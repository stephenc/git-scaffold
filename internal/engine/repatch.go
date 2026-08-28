package engine

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"sigs.k8s.io/yaml"

	"github.com/stephenc/git-scaffold/internal/config"
	"github.com/stephenc/git-scaffold/internal/materialize"
)

// patchChoice is the outcome of choosePatch for one differing managed file.
// strategy is "" when the difference is purely structured formatting and no
// override is needed; content is the worktree content after the override is
// in place (the canonical serialization for json-patch, the wanted bytes
// verbatim for text-patch, the base bytes when no override is needed).
type patchChoice struct {
	strategy string
	patch    []byte
	content  []byte
}

func structuredExt(path string) bool {
	return strings.HasSuffix(path, ".json") || strings.HasSuffix(path, ".yml") || strings.HasSuffix(path, ".yaml")
}

// choosePatch selects the override strategy that captures want against base
// for a managed file (§33, §57), shared by init --existing and repatch. A
// json-patch is preferred when the rule permits it, the file has a
// structured extension, both sides parse, and the generated patch verifies
// through the real applier; a difference that vanishes after decoding is a
// formatting-only difference needing no override. Everything else, and
// textOnly, yields a verified text-patch.
func choosePatch(path string, base, want []byte, permitted string, textOnly bool) (patchChoice, error) {
	if !textOnly && permitted == config.StrategyJSONPatch && structuredExt(path) {
		if c, ok := chooseJSONPatch(path, base, want); ok {
			return c, nil
		}
	}
	rel := "patches/" + strings.ReplaceAll(path, "/", "--") + ".patch"
	patch := []byte(unifiedDiff(path, base, want, true))
	// The guarantee hinges on the generated diff applying through the
	// strict text-patch applier and reproducing the wanted bytes exactly;
	// verify before anything is written.
	got, err := materialize.ApplyTextPatch(path, rel, base, patch)
	if err != nil {
		return patchChoice{}, fmt.Errorf("generated patch for %s does not apply: %w", path, err)
	}
	if !bytes.Equal(got, want) {
		return patchChoice{}, fmt.Errorf("generated patch for %s does not reproduce the existing content", path)
	}
	return patchChoice{strategy: config.StrategyTextPatch, patch: patch, content: want}, nil
}

// chooseJSONPatch attempts the structured route of choosePatch; ok is false
// whenever text-patch must be used instead. For YAML the route is taken
// only when the base already round-trips byte-for-byte through the
// YAML→JSON→YAML canonicalization: json-patch output canonicalizes the whole
// file (comments dropped, anchors expanded, YAML 1.1 scalars such as `on`,
// `yes` or `0755` resolved), and that must never rewrite keys the user did
// not touch.
func chooseJSONPatch(path string, base, want []byte) (patchChoice, bool) {
	if !strings.HasSuffix(path, ".json") && !yamlCanonical(base) {
		return patchChoice{}, false
	}
	from, err := materialize.DecodeStructured(path, base)
	if err != nil {
		return patchChoice{}, false
	}
	to, err := materialize.DecodeStructured(path, want)
	if err != nil {
		return patchChoice{}, false
	}
	if from == nil || to == nil {
		// An empty or comment-only document has no useful structure.
		return patchChoice{}, false
	}
	if reflect.DeepEqual(from, to) {
		return patchChoice{content: base}, true
	}
	patch, err := materialize.GenerateJSONPatch(path, base, want)
	if err != nil {
		return patchChoice{}, false
	}
	got, err := materialize.ApplyJSONPatch(path, "patches/generated.json", base, patch)
	if err != nil {
		return patchChoice{}, false
	}
	gotDoc, err := materialize.DecodeStructured(path, got)
	if err != nil || !reflect.DeepEqual(gotDoc, to) {
		return patchChoice{}, false
	}
	return patchChoice{strategy: config.StrategyJSONPatch, patch: patch, content: got}, true
}

// yamlCanonical reports whether YAML content is unchanged by the
// YAML→JSON→YAML canonicalization json-patch application performs.
func yamlCanonical(content []byte) bool {
	j, err := yaml.YAMLToJSON(content)
	if err != nil {
		return false
	}
	y, err := yaml.JSONToYAML(j)
	return err == nil && bytes.Equal(y, content)
}

// freePatchPath mangles a repo-relative path into a patch file path relative
// to .git-scaffold/ (`/` becomes `--`, extension by strategy), with a numeric
// suffix on the rare collision with an already used path. The chosen path is
// marked used.
func freePatchPath(path, strategy string, used map[string]bool) string {
	ext := ".patch"
	if strategy == config.StrategyJSONPatch {
		ext = ".json"
	}
	base := "patches/" + strings.ReplaceAll(path, "/", "--")
	rel := base + ext
	for i := 2; used[rel]; i++ {
		rel = fmt.Sprintf("%s-%d%s", base, i, ext)
	}
	used[rel] = true
	return rel
}

// Repatch implements `git scaffold repatch` (§57): rewrite the target's
// overrides so that the current working-tree content of every managed file
// is what materialization produces, i.e. so that `check` passes afterwards.
// Only files whose worktree content differs from the current full
// materialization are re-derived; each gets exactly one regenerated patch
// (json-patch where permitted and parseable unless textOnly, else
// text-patch); files back at their base content lose their override; stale
// patch files are deleted.
// json-patched files are normalized to the canonical serialization. The
// lock is never touched and no --force exists: capturing hand edits is the
// whole point.
func Repatch(dir string, out io.Writer, textOnly bool) error {
	t, err := load(dir)
	if err != nil {
		return err
	}
	sha, err := readLock(t.root)
	if err != nil {
		return err
	}
	if sha == "" {
		return fmt.Errorf("no %s; run `git scaffold apply` first", lockName)
	}
	srcTree, desc, err := t.loadSource(sha)
	if err != nil {
		return err
	}
	baseCfg := *t.cfg
	baseCfg.Overrides = map[string]config.Override{}
	base, err := t.materializeTree(srcTree, desc, &baseCfg, nil)
	if err != nil {
		return err
	}
	permitted, err := materialize.PermittedPatch(srcTree, desc)
	if err != nil {
		return err
	}
	// Existing patch files are read leniently: a missing one is simply
	// regenerated rather than blocking the command that exists to fix
	// broken overrides.
	oldPatches := map[string][]byte{}
	for _, o := range t.cfg.Overrides {
		for _, pp := range o.Patches {
			data, err := os.ReadFile(filepath.Join(t.root, metaDir, filepath.FromSlash(pp)))
			if err == nil {
				oldPatches[pp] = data
			} else if !os.IsNotExist(err) {
				return fmt.Errorf("patch file %s/%s: %w", metaDir, pp, err)
			}
		}
	}

	// The current full materialization decides which files need work at
	// all: a file the existing overrides already reproduce is left alone,
	// override included. When it cannot be computed (a broken override or
	// patch — what repatch exists to fix) every file is re-derived.
	current, err := t.materializeTree(srcTree, desc, t.cfg, oldPatches)
	if err != nil {
		current = nil
	}
	// Patch paths referenced by more than one override cannot be rewritten
	// in place for either of them.
	refCount := map[string]int{}
	for _, o := range t.cfg.Overrides {
		for _, pp := range o.Patches {
			refCount[pp]++
		}
	}

	newOverrides := map[string]config.Override{}
	patchFiles := map[string][]byte{}
	for k, v := range oldPatches {
		patchFiles[k] = v
	}
	writes := map[string][]byte{}
	expect := map[string][]byte{}
	var patched, normalized, unpatched, missing, binaries []string
	for _, p := range sortedKeys(base) {
		w, ok, err := readWorking(t.root, p)
		if err != nil {
			return err
		}
		old, hadOverride := t.cfg.Overrides[p]
		switch {
		case !ok:
			missing = append(missing, p)
			continue
		case current != nil && bytes.Equal(w, current[p]):
			if hadOverride {
				newOverrides[p] = old
			}
			expect[p] = w
			continue
		case bytes.Equal(w, base[p]):
			if hadOverride {
				unpatched = append(unpatched, p)
			}
			expect[p] = w
			continue
		case isBinary(base[p]) || isBinary(w):
			binaries = append(binaries, p)
			continue
		}
		c, err := choosePatch(p, base[p], w, permitted[p], textOnly)
		if err != nil {
			return fmt.Errorf("repatch: %w", err)
		}
		if c.strategy == "" {
			// Formatting-only difference: no override, normalize.
			if hadOverride {
				unpatched = append(unpatched, p)
			}
			writes[p] = base[p]
			normalized = append(normalized, p)
			expect[p] = base[p]
			continue
		}
		expect[p] = c.content
		if !bytes.Equal(c.content, w) {
			writes[p] = c.content
			normalized = append(normalized, p)
		}
		reuse := hadOverride && old.Strategy == c.strategy && len(old.Patches) == 1 &&
			refCount[old.Patches[0]] == 1
		if reuse && bytes.Equal(oldPatches[old.Patches[0]], c.patch) {
			newOverrides[p] = old
			continue
		}
		var rel string
		if reuse {
			rel = old.Patches[0]
		} else {
			// Never collide with a patch path any other override refers to,
			// whether it survives this run or not.
			used := map[string]bool{}
			for op, o := range t.cfg.Overrides {
				if op != p {
					for _, pp := range o.Patches {
						used[pp] = true
					}
				}
			}
			for op, o := range newOverrides {
				if op != p {
					for _, pp := range o.Patches {
						used[pp] = true
					}
				}
			}
			rel = freePatchPath(p, c.strategy, used)
		}
		patchFiles[rel] = c.patch
		writes[metaDir+"/"+rel] = c.patch
		newOverrides[p] = config.Override{Strategy: c.strategy, Patches: []string{rel}}
		patched = append(patched, p)
	}
	if len(missing) > 0 {
		return fmt.Errorf("cannot repatch missing managed files (run `git scaffold apply` first):\n  %s",
			strings.Join(missing, "\n  "))
	}
	if len(binaries) > 0 {
		return fmt.Errorf("cannot repatch binary files (no meaningful patch exists):\n  %s",
			strings.Join(binaries, "\n  "))
	}
	// Overrides of paths the source does not manage cannot survive: they
	// would fail every materialization.
	for _, p := range sortedKeys(t.cfg.Overrides) {
		if _, managed := base[p]; !managed {
			unpatched = append(unpatched, p)
		}
	}
	sort.Strings(unpatched)

	// Stale patch files: referenced before, by nobody now.
	referenced := map[string]bool{}
	for _, o := range newOverrides {
		for _, pp := range o.Patches {
			referenced[pp] = true
		}
	}
	var deletes, deleted []string
	for _, pp := range sortedKeys(oldPatches) {
		if !referenced[pp] {
			deletes = append(deletes, metaDir+"/"+pp)
			deleted = append(deleted, metaDir+"/"+pp)
			delete(patchFiles, pp)
		}
	}

	// Verify before writing anything (§31): the full materialization with
	// the new overrides must be exactly the post-repatch worktree.
	newCfg := *t.cfg
	newCfg.Overrides = newOverrides
	full, err := t.materializeTree(srcTree, desc, &newCfg, patchFiles)
	if err != nil {
		return fmt.Errorf("repatch: verification failed: %w", err)
	}
	if err := verifyExpected(full, expect); err != nil {
		return fmt.Errorf("repatch: verification failed: %w", err)
	}

	if len(patched)+len(normalized)+len(unpatched)+len(deleted) == 0 {
		io.WriteString(out, status(true, "patches already up to date"))
		return nil
	}
	if !reflect.DeepEqual(newOverrides, t.cfg.Overrides) {
		original, err := os.ReadFile(filepath.Join(t.root, metaDir, "config.toml"))
		if err != nil {
			return err
		}
		rewritten, err := rewriteOverrides(original, newOverrides)
		if err != nil {
			return err
		}
		writes[configName] = rewritten
	}
	if err := commit(t.root, writes, deletes, ""); err != nil {
		return err
	}
	for _, p := range patched {
		fmt.Fprintf(out, "P %s (%s)\n", p, newOverrides[p].Strategy)
	}
	for _, p := range normalized {
		fmt.Fprintf(out, "M %s\n", p)
	}
	for _, p := range unpatched {
		fmt.Fprintf(out, "U %s\n", p)
	}
	for _, p := range deleted {
		fmt.Fprintf(out, "D %s\n", p)
	}
	io.WriteString(out, status(true, "updated patches"))
	return nil
}

// overridesHeader matches a TOML table header belonging to [overrides]:
// the parent table itself or any [overrides."path"] sub-table.
var overridesHeader = regexp.MustCompile(`^\s*\[\s*overrides\s*(\]|\.)`)

// rewriteOverrides replaces the [overrides] section of a target
// configuration with the given set while preserving the user's file —
// comments, ordering, formatting — everywhere else. Every overrides table
// header and the lines following it up to the next unrelated table header
// are removed; the new overrides (if any) are appended in canonical TOML.
// The result is validated by parsing: when it does not reproduce the
// desired configuration (for example because the original used an inline
// `overrides = {...}` table), the whole file is re-encoded from the parsed
// configuration instead.
func rewriteOverrides(original []byte, overrides map[string]config.Override) ([]byte, error) {
	orig, err := config.ParseTarget(original)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", configName, err)
	}
	// Work on LF internally; a CRLF file gets CRLF back throughout.
	crlf := bytes.Contains(original, []byte("\r\n"))
	if crlf {
		original = bytes.ReplaceAll(original, []byte("\r\n"), []byte("\n"))
	}
	lines := strings.Split(string(original), "\n")
	var kept []string
	skipping := false
	for _, line := range lines {
		switch {
		case overridesHeader.MatchString(line):
			skipping = true
			continue
		case skipping && strings.HasPrefix(strings.TrimLeft(line, " \t"), "["):
			skipping = false
		}
		if !skipping {
			kept = append(kept, line)
		}
	}
	text := strings.TrimRight(strings.Join(kept, "\n"), "\n") + "\n"
	if len(overrides) > 0 {
		var enc struct {
			Overrides map[string]tomlOverride `toml:"overrides"`
		}
		enc.Overrides = map[string]tomlOverride{}
		for p, o := range overrides {
			enc.Overrides[p] = tomlOverride{Strategy: o.Strategy, Patches: o.Patches}
		}
		var buf bytes.Buffer
		if err := toml.NewEncoder(&buf).Encode(enc); err != nil {
			return nil, err
		}
		text += "\n" + buf.String()
	}

	if crlf {
		text = strings.ReplaceAll(text, "\n", "\r\n")
	}
	if got, err := config.ParseTarget([]byte(text)); err == nil && sameConfig(got, orig, overrides) {
		return []byte(text), nil
	}

	// Fallback: full canonical re-encode.
	var enc tomlTarget
	enc.Scaffold.Version = config.Version
	enc.Source.Git = orig.SourceGit
	enc.Source.Ref = orig.SourceRef
	enc.Args = orig.Args
	if len(overrides) > 0 {
		enc.Overrides = map[string]tomlOverride{}
		for p, o := range overrides {
			enc.Overrides[p] = tomlOverride{Strategy: o.Strategy, Patches: o.Patches}
		}
	}
	if orig.ProposeCreateCommand != nil {
		enc.Propose = &tomlPropose{CreateCommand: orig.ProposeCreateCommand}
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(enc); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')
	out := buf.Bytes()
	if crlf {
		out = bytes.ReplaceAll(out, []byte("\n"), []byte("\r\n"))
	}
	got, err := config.ParseTarget(out)
	if err != nil || !sameConfig(got, orig, overrides) {
		return nil, fmt.Errorf("%s: cannot rewrite overrides", configName)
	}
	return out, nil
}

// sameConfig reports whether got carries the desired overrides and is
// otherwise identical to orig.
func sameConfig(got, orig *config.TargetConfig, overrides map[string]config.Override) bool {
	if got.SourceGit != orig.SourceGit || got.SourceRef != orig.SourceRef ||
		!reflect.DeepEqual(got.Args, orig.Args) ||
		!reflect.DeepEqual(got.ProposeCreateCommand, orig.ProposeCreateCommand) {
		return false
	}
	if len(got.Overrides) != len(overrides) {
		return false
	}
	for p, o := range overrides {
		g, ok := got.Overrides[p]
		if !ok || g.Strategy != o.Strategy || !reflect.DeepEqual(g.Patches, o.Patches) {
			return false
		}
	}
	return true
}
