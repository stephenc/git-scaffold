// Package materialize is the pure materialization core of DESIGN.md §52:
// source tree + descriptor + target config + patch files in, expected managed
// tree out. It performs no git operations and no working-tree mutation.
package materialize

import (
	"fmt"
	"sort"
	"strings"

	"github.com/stephenc/git-scaffold/internal/config"
	"github.com/stephenc/git-scaffold/internal/glob"
)

// argEffect is the effective behavior of one argument under one file rule,
// after §17 resolution (rule token → global token → none; disabled rules and
// substitute = false yield no substitution).
type argEffect struct {
	enabled bool
	token   string // "" when no substitution occurs
}

// ruleEffect is the effective per-file behavior a rule implies. An exact rule
// governs a file alone even when globs also match it (§9.5); two rules of the
// same kind may match the same file only if their ruleEffects are equal; rules that
// differ only in ways without per-file effect (e.g. allow-empty, or an
// explicit rule token equal to the global token) are not in conflict. The
// substitute flag itself is compared even when no argument makes it
// observable, so a descriptor cannot become ambiguous merely by gaining an
// argument.
type ruleEffect struct {
	patch      string
	substitute bool
	args       map[string]argEffect
}

func (e ruleEffect) equal(o ruleEffect) bool {
	if e.patch != o.patch || e.substitute != o.substitute || len(e.args) != len(o.args) {
		return false
	}
	for k, v := range e.args {
		if o.args[k] != v {
			return false
		}
	}
	return true
}

func effect(d *config.Descriptor, rule *config.FileRule) ruleEffect {
	e := ruleEffect{patch: rule.Patch, substitute: rule.Substitute, args: map[string]argEffect{}}
	for _, a := range d.Arguments {
		eff := argEffect{enabled: rule.Substitute, token: a.Token}
		if r, ok := rule.Arguments[a.Name]; ok {
			if !r.Enabled {
				eff.enabled = false
			}
			if r.Token != nil {
				eff.token = *r.Token
			}
		}
		if !eff.enabled || eff.token == "" {
			eff = argEffect{}
		}
		e.args[a.Name] = eff
	}
	return e
}

// resolveArgs resolves every declared argument to a value: target value,
// else source default, else the argument joins the missing list (§11).
// Target values for undeclared arguments are rejected as a source/target
// contract mismatch.
func resolveArgs(d *config.Descriptor, t *config.TargetConfig) (map[string]string, error) {
	for _, name := range sortedKeys(t.Args) {
		if d.Argument(name) == nil {
			return nil, fmt.Errorf("argument %q is not declared by the source descriptor", name)
		}
	}
	values := map[string]string{}
	var missing []string
	for _, a := range d.Arguments {
		if v, ok := t.Args[a.Name]; ok {
			values[a.Name] = v
		} else if a.Default != nil {
			values[a.Name] = *a.Default
		} else {
			missing = append(missing, a.Name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("missing required arguments: %s", strings.Join(missing, ", "))
	}
	return values, nil
}

// expand resolves the descriptor's file rules against the source tree paths
// into the concrete managed set (§25): a sorted list of paths, each with the
// effective behavior of its governing rule.
func expand(sourceTree map[string][]byte, d *config.Descriptor) (map[string]ruleEffect, error) {
	paths := sortedKeys(sourceTree)
	managed := map[string]ruleEffect{}

	// §9.5: an exact rule is more specific than a glob, so exact rules are
	// expanded first and a glob match never displaces one. The identical-
	// configuration requirement applies only between rules of the same kind.
	exact := map[string]bool{}
	for i := range d.Files {
		rule := &d.Files[i]
		if glob.HasMeta(rule.Path) {
			continue
		}
		// §9.2/§9.3: a missing exact path is an error that allow-empty
		// does not suppress.
		if _, ok := sourceTree[rule.Path]; !ok {
			return nil, fmt.Errorf("managed file %q does not exist in the source tree", rule.Path)
		}
		e := effect(d, rule)
		if prev, ok := managed[rule.Path]; ok {
			if !prev.equal(e) {
				return nil, fmt.Errorf(
					"ambiguous descriptor: overlapping rules match %q with different effective configuration", rule.Path)
			}
			continue
		}
		managed[rule.Path] = e
		exact[rule.Path] = true
	}

	for i := range d.Files {
		rule := &d.Files[i]
		if !glob.HasMeta(rule.Path) {
			continue
		}
		matches := glob.Expand(rule.Path, paths)
		if len(matches) == 0 && !rule.AllowEmpty {
			return nil, fmt.Errorf("pattern %q matches no files in the source tree", rule.Path)
		}
		e := effect(d, rule)
		for _, m := range matches {
			if exact[m] {
				continue
			}
			if prev, ok := managed[m]; ok {
				if !prev.equal(e) {
					return nil, fmt.Errorf(
						"ambiguous descriptor: overlapping rules match %q with different effective configuration", m)
				}
				continue
			}
			managed[m] = e
		}
	}
	return managed, nil
}

// fileTokens builds the effective token → value map for one concrete file,
// enforcing §19: two enabled arguments must not share an effective token
// within a single file.
func fileTokens(path string, e ruleEffect, values map[string]string) (map[string]string, error) {
	tokens := map[string]string{}
	owner := map[string]string{}
	for _, name := range sortedKeys(e.args) {
		a := e.args[name]
		if !a.enabled {
			continue
		}
		if prev, ok := owner[a.token]; ok {
			return nil, fmt.Errorf(
				"%s: arguments %q and %q resolve to the same effective token %q", path, prev, name, a.token)
		}
		owner[a.token] = name
		tokens[a.token] = values[name]
	}
	return tokens, nil
}

// Materialize computes the expected managed tree (§25): expand exact paths
// and globs against the source tree, resolve argument values, substitute
// tokens simultaneously, then apply target overrides. patchFiles is keyed by
// path relative to .git-scaffold/. The input maps are not modified.
func Materialize(
	sourceTree map[string][]byte,
	d *config.Descriptor,
	t *config.TargetConfig,
	patchFiles map[string][]byte,
) (map[string][]byte, error) {
	managed, err := expand(sourceTree, d)
	if err != nil {
		return nil, err
	}
	values, err := resolveArgs(d, t)
	if err != nil {
		return nil, err
	}

	out := map[string][]byte{}
	for _, path := range sortedKeys(managed) {
		tokens, err := fileTokens(path, managed[path], values)
		if err != nil {
			return nil, err
		}
		out[path] = substitute(sourceTree[path], tokens)
	}

	// Overrides apply after substitution (§25), in patch declaration order
	// (§24). Override paths are iterated sorted for deterministic error
	// selection.
	for _, path := range sortedKeys(t.Overrides) {
		o := t.Overrides[path]
		e, ok := managed[path]
		if !ok {
			return nil, fmt.Errorf("%s: override targets a file the source does not manage", path)
		}
		// §23: text-patch is always available as the universal escape hatch;
		// a structured strategy requires the rule's patch declaration.
		if o.Strategy != config.StrategyTextPatch {
			if e.patch == "" {
				return nil, fmt.Errorf("%s: the source does not permit structured patching of this file", path)
			}
			if o.Strategy != e.patch {
				return nil, fmt.Errorf("%s: override strategy %q does not match source-permitted strategy %q",
					path, o.Strategy, e.patch)
			}
		}
		content := out[path]
		for _, pp := range o.Patches {
			data, ok := patchFiles[pp]
			if !ok {
				return nil, fmt.Errorf("%s: patch file %s not found under .git-scaffold/", path, pp)
			}
			switch o.Strategy {
			case config.StrategyJSONPatch:
				content, err = applyJSONPatch(path, pp, content, data)
			case config.StrategyTextPatch:
				content, err = ApplyTextPatch(path, pp, content, data)
			default:
				err = fmt.Errorf("%s: %s: unsupported patch strategy %q", path, pp, o.Strategy)
			}
			if err != nil {
				return nil, err
			}
		}
		out[path] = content
	}
	return out, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
