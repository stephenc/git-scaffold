// Package config parses and validates the shared TOML configuration format
// of DESIGN.md: the source descriptor (§8, §10, §13-§16) and the target
// configuration (§4-§6, §11, §23-§24, §44). The same [scaffold] version
// header governs both (§5).
package config

import (
	"fmt"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/stephenc/git-scaffold/internal/glob"
)

// Version is the sole supported configuration format version (§5).
const Version = 1

// Patch strategies supported in v0.1 (§26, §27).
const (
	StrategyJSONPatch = "json-patch"
	StrategyTextPatch = "text-patch"
)

// Argument is one [[arguments]] declaration of a source descriptor (§10).
type Argument struct {
	Name        string
	Description string
	// Default is nil when absent; an argument without a default is required.
	Default *string
	// Token is the global literal token, or "" when the argument declares
	// none (§14).
	Token string
}

// ArgumentRule is a per-rule [files.arguments.NAME] override (§13, §15).
type ArgumentRule struct {
	// Token is nil when the rule does not override the token.
	Token *string
	// Enabled defaults to true (§15).
	Enabled bool
}

// FileRule is one [[files]] entry of a source descriptor (§8, §9).
type FileRule struct {
	Path string
	// Patch is the permitted patch strategy, or "" when the rule permits no
	// target patching (§23).
	Patch string
	// Substitute defaults to true (§16).
	Substitute bool
	// AllowEmpty defaults to false and only affects glob rules (§9.3).
	AllowEmpty bool
	// Arguments holds per-rule argument overrides keyed by argument name.
	Arguments map[string]ArgumentRule
}

// Descriptor is a validated source descriptor.
type Descriptor struct {
	TemplateName string
	Arguments    []Argument
	Files        []FileRule
}

// Argument returns the declared argument with the given name, or nil.
func (d *Descriptor) Argument(name string) *Argument {
	for i := range d.Arguments {
		if d.Arguments[i].Name == name {
			return &d.Arguments[i]
		}
	}
	return nil
}

// Override is one [overrides."path"] entry of a target configuration (§24).
type Override struct {
	Strategy string
	// Patches are paths relative to .git-scaffold/, applied in declaration
	// order.
	Patches []string
}

// TargetConfig is a validated target configuration.
type TargetConfig struct {
	SourceGit string
	SourceRef string
	Args      map[string]string
	// Overrides is keyed by concrete repository-relative target path.
	Overrides map[string]Override
	// ProposeCreateCommand is the optional [propose] create-command argument
	// array (§44).
	ProposeCreateCommand []string
}

// Raw TOML shapes. Pointer fields distinguish "absent" from a zero value so
// defaults and explicit-but-empty errors can be told apart.

type rawScaffold struct {
	Version *int64 `toml:"version"`
}

type rawTemplate struct {
	Name string `toml:"name"`
}

type rawArgument struct {
	Name        string  `toml:"name"`
	Description string  `toml:"description"`
	Default     *string `toml:"default"`
	Token       *string `toml:"token"`
}

type rawArgumentRule struct {
	Token   *string `toml:"token"`
	Enabled *bool   `toml:"enabled"`
}

type rawFile struct {
	Path       string                     `toml:"path"`
	Patch      *string                    `toml:"patch"`
	Substitute *bool                      `toml:"substitute"`
	AllowEmpty *bool                      `toml:"allow-empty"`
	Arguments  map[string]rawArgumentRule `toml:"arguments"`
}

type rawDescriptor struct {
	Scaffold  *rawScaffold  `toml:"scaffold"`
	Template  *rawTemplate  `toml:"template"`
	Arguments []rawArgument `toml:"arguments"`
	Files     []rawFile     `toml:"files"`
}

type rawSource struct {
	Git string `toml:"git"`
	Ref string `toml:"ref"`
}

type rawOverride struct {
	Strategy string   `toml:"strategy"`
	Patches  []string `toml:"patches"`
}

type rawPropose struct {
	CreateCommand []string `toml:"create-command"`
}

type rawTarget struct {
	Scaffold  *rawScaffold           `toml:"scaffold"`
	Source    *rawSource             `toml:"source"`
	Args      map[string]string      `toml:"args"`
	Overrides map[string]rawOverride `toml:"overrides"`
	Propose   *rawPropose            `toml:"propose"`
}

func checkVersion(s *rawScaffold) error {
	if s == nil || s.Version == nil {
		return fmt.Errorf("missing required [scaffold] version")
	}
	if *s.Version != Version {
		return fmt.Errorf("unsupported scaffold version %d (supported: %d)", *s.Version, Version)
	}
	return nil
}

// checkRepoRelative rejects absolute and repository-escaping paths (§48).
// Paths are forward-slash repo-relative on every platform: backslash
// separators, Windows drive prefixes (`C:/...`, `C:foo`), and UNC paths
// (`\\host\...`) are all rejected.
func checkRepoRelative(kind, path string) error {
	if path == "" {
		return fmt.Errorf("%s must not be empty", kind)
	}
	if strings.ContainsRune(path, '\\') {
		return fmt.Errorf("%s %q must use forward slashes, not backslashes", kind, path)
	}
	if strings.HasPrefix(path, "/") {
		return fmt.Errorf("%s %q must not be absolute", kind, path)
	}
	if len(path) >= 2 && path[1] == ':' && isDriveLetter(path[0]) {
		return fmt.Errorf("%s %q must not carry a drive prefix", kind, path)
	}
	for _, c := range strings.Split(path, "/") {
		if c == ".." {
			return fmt.Errorf("%s %q must not escape the repository", kind, path)
		}
	}
	return nil
}

func isDriveLetter(b byte) bool {
	return ('a' <= b && b <= 'z') || ('A' <= b && b <= 'Z')
}

func checkStrategy(context, strategy string) error {
	switch strategy {
	case StrategyJSONPatch, StrategyTextPatch:
		return nil
	case "":
		return fmt.Errorf("%s: missing patch strategy", context)
	default:
		return fmt.Errorf("%s: unsupported patch strategy %q (supported: %s, %s)",
			context, strategy, StrategyJSONPatch, StrategyTextPatch)
	}
}

// ParseDescriptor parses and validates a source descriptor.
func ParseDescriptor(data []byte) (*Descriptor, error) {
	var raw rawDescriptor
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("invalid TOML: %w", err)
	}
	if err := checkVersion(raw.Scaffold); err != nil {
		return nil, err
	}

	d := &Descriptor{}
	if raw.Template != nil {
		d.TemplateName = raw.Template.Name
	}

	seen := map[string]bool{}
	for i, a := range raw.Arguments {
		if a.Name == "" {
			return nil, fmt.Errorf("arguments[%d]: missing required name", i)
		}
		if seen[a.Name] {
			return nil, fmt.Errorf("duplicate argument name %q", a.Name)
		}
		seen[a.Name] = true
		arg := Argument{Name: a.Name, Description: a.Description, Default: a.Default}
		if a.Token != nil {
			if *a.Token == "" {
				return nil, fmt.Errorf("argument %q: explicit token must not be empty", a.Name)
			}
			arg.Token = *a.Token
		}
		d.Arguments = append(d.Arguments, arg)
	}

	for i, f := range raw.Files {
		if f.Path == "" {
			return nil, fmt.Errorf("files[%d]: missing required path", i)
		}
		if err := checkRepoRelative("file pattern", f.Path); err != nil {
			return nil, err
		}
		rule := FileRule{Path: f.Path, Substitute: true}
		if f.Substitute != nil {
			rule.Substitute = *f.Substitute
		}
		if f.AllowEmpty != nil {
			rule.AllowEmpty = *f.AllowEmpty
		}
		if f.Patch != nil {
			if err := checkStrategy(fmt.Sprintf("files %q", f.Path), *f.Patch); err != nil {
				return nil, err
			}
			rule.Patch = *f.Patch
		}
		if len(f.Arguments) > 0 {
			// §16: substitution is disabled wholesale, so any per-rule
			// argument configuration is contradictory, not merely inert.
			if !rule.Substitute {
				return nil, fmt.Errorf(
					"files %q: substitute = false cannot be combined with per-rule argument configuration",
					f.Path)
			}
			rule.Arguments = map[string]ArgumentRule{}
			// Sorted for deterministic error selection.
			names := make([]string, 0, len(f.Arguments))
			for name := range f.Arguments {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				if !seen[name] {
					return nil, fmt.Errorf("files %q: references undefined argument %q", f.Path, name)
				}
				r := f.Arguments[name]
				ar := ArgumentRule{Enabled: true}
				if r.Enabled != nil {
					ar.Enabled = *r.Enabled
				}
				if r.Token != nil {
					if *r.Token == "" {
						return nil, fmt.Errorf(
							"files %q: argument %q: explicit token must not be empty", f.Path, name)
					}
					ar.Token = r.Token
				}
				rule.Arguments[name] = ar
			}
		}
		d.Files = append(d.Files, rule)
	}
	return d, nil
}

// ParseTarget parses and validates a target configuration.
func ParseTarget(data []byte) (*TargetConfig, error) {
	var raw rawTarget
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("invalid TOML: %w", err)
	}
	if err := checkVersion(raw.Scaffold); err != nil {
		return nil, err
	}
	if raw.Source == nil || raw.Source.Git == "" {
		return nil, fmt.Errorf("missing required [source] git")
	}

	t := &TargetConfig{
		SourceGit: raw.Source.Git,
		SourceRef: raw.Source.Ref,
		Args:      map[string]string{},
		Overrides: map[string]Override{},
	}
	for k, v := range raw.Args {
		t.Args[k] = v
	}

	// Sorted for deterministic error selection.
	paths := make([]string, 0, len(raw.Overrides))
	for p := range raw.Overrides {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		// §24: override keys are concrete paths; target-side globs are out
		// of scope for v0.1.
		if glob.HasMeta(p) {
			return nil, fmt.Errorf("overrides %q: override keys must be concrete paths, not globs", p)
		}
		if err := checkRepoRelative("override path", p); err != nil {
			return nil, err
		}
		o := raw.Overrides[p]
		if err := checkStrategy(fmt.Sprintf("overrides %q", p), o.Strategy); err != nil {
			return nil, err
		}
		for _, pp := range o.Patches {
			// §48: patch paths are relative to and must remain beneath
			// .git-scaffold/.
			if err := checkRepoRelative("patch path", pp); err != nil {
				return nil, fmt.Errorf("overrides %q: %w", p, err)
			}
		}
		t.Overrides[p] = Override{Strategy: o.Strategy, Patches: append([]string(nil), o.Patches...)}
	}

	if raw.Propose != nil {
		t.ProposeCreateCommand = append([]string(nil), raw.Propose.CreateCommand...)
	}
	return t, nil
}
