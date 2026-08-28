package engine

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stephenc/git-scaffold/internal/config"
)

// descriptorR manages a json-patch-permitted JSON file and YAML file, a
// structured file without patch permission, and a plain text file.
const descriptorR = `
[scaffold]
version = 1

[[arguments]]
name = "name"
token = "@@NAME@@"

[[files]]
path = "config.json"
patch = "json-patch"

[[files]]
path = "settings.yaml"
patch = "json-patch"

[[files]]
path = "data.json"

[[files]]
path = "Makefile"
`

const (
	baseConfigJSON   = "{\n  \"name\": \"@@NAME@@\",\n  \"timeout\": 5,\n  \"list\": [\n    1,\n    2\n  ]\n}\n"
	baseSettingsYAML = "linters:\n- govet\nrun:\n  timeout: 5m\n" // canonical: round-trips YAML→JSON→YAML
	baseDataJSON     = "{\n  \"x\": 1\n}\n"
	baseMakefile     = "build:\n\tgo build ./...\n"
)

var repatchArgs = map[string]string{"name": "orders"}

func sourceRepoR(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)
	writeFiles(t, dir, map[string]string{
		".git-scaffold/config.toml": descriptorR,
		"config.json":               baseConfigJSON,
		"settings.yaml":             baseSettingsYAML,
		"data.json":                 baseDataJSON,
		"Makefile":                  baseMakefile,
	})
	commitAll(t, dir, "R")
	return dir
}

// initializedR returns a target initialized from source R with no overrides.
func initializedR(t *testing.T) (src, tgt string) {
	t.Helper()
	src = sourceRepoR(t)
	tgt = newRepo(t)
	if err := Init(tgt, io.Discard, filepath.ToSlash(src), "main", repatchArgs, false, false, false); err != nil {
		t.Fatal(err)
	}
	mustCheckClean(t, tgt)
	return src, tgt
}

func mustRepatch(t *testing.T, dir string, textOnly bool) string {
	t.Helper()
	var out bytes.Buffer
	if err := Repatch(dir, &out, textOnly); err != nil {
		t.Fatalf("repatch: %v\n%s", err, out.String())
	}
	return out.String()
}

func parseTargetConfig(t *testing.T, dir string) *config.TargetConfig {
	t.Helper()
	cfg, err := config.ParseTarget([]byte(readFile(t, dir, ".git-scaffold/config.toml")))
	if err != nil {
		t.Fatalf("config.toml: %v", err)
	}
	return cfg
}

func exists(t *testing.T, dir, rel string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
	if err == nil {
		return true
	}
	if os.IsNotExist(err) {
		return false
	}
	t.Fatal(err)
	return false
}

func TestRepatchJSONHandEdit(t *testing.T) {
	setEnv(t)
	_, tgt := initializedR(t)
	// Hand edit with non-canonical formatting: a json patch is generated
	// and the worktree is normalized to the canonical serialization.
	writeFiles(t, tgt, map[string]string{"config.json": `{"name":"orders","timeout":10,"list":[1,2,3]}`})
	out := mustRepatch(t, tgt, false)
	if !strings.Contains(out, "P config.json (json-patch)\n") || !strings.Contains(out, "M config.json\n") ||
		!strings.Contains(out, "updated patches") {
		t.Fatalf("output:\n%s", out)
	}
	patch := readFile(t, tgt, ".git-scaffold/patches/config.json.json")
	if !strings.Contains(patch, `"/timeout"`) || !strings.Contains(patch, `"/list/2"`) {
		t.Fatalf("patch:\n%s", patch)
	}
	cfg := parseTargetConfig(t, tgt)
	want := config.Override{Strategy: "json-patch", Patches: []string{"patches/config.json.json"}}
	if !reflect.DeepEqual(cfg.Overrides["config.json"], want) {
		t.Fatalf("overrides = %+v", cfg.Overrides)
	}
	got := readFile(t, tgt, "config.json")
	if !strings.Contains(got, "\n  \"timeout\": 10") || !strings.HasSuffix(got, "\n") {
		t.Fatalf("config.json not normalized: %q", got)
	}
	mustCheckClean(t, tgt)
}

func TestRepatchYAMLHandEdit(t *testing.T) {
	setEnv(t)
	_, tgt := initializedR(t)
	writeFiles(t, tgt, map[string]string{"settings.yaml": "run:\n  timeout: 10m\n  tests: true\nlinters:\n  - govet\n  - staticcheck\n"})
	// A canonical base with a hand edit takes the json route; the edit is
	// normalized to the canonical serialization.
	out := mustRepatch(t, tgt, false)
	if !strings.Contains(out, "P settings.yaml (json-patch)\n") {
		t.Fatalf("output:\n%s", out)
	}
	patch := readFile(t, tgt, ".git-scaffold/patches/settings.yaml.json")
	if !strings.Contains(patch, `"/run/tests"`) || !strings.Contains(patch, `"/linters/1"`) {
		t.Fatalf("patch:\n%s", patch)
	}
	if got := readFile(t, tgt, "settings.yaml"); !strings.Contains(got, "timeout: 10m") {
		t.Fatalf("settings.yaml = %q", got)
	}
	mustCheckClean(t, tgt)
}

func TestRepatchNotPermittedUsesText(t *testing.T) {
	setEnv(t)
	_, tgt := initializedR(t)
	local := `{"x": 2}`
	writeFiles(t, tgt, map[string]string{"data.json": local, "Makefile": baseMakefile + "\nlint:\n\tgo vet ./...\n"})
	out := mustRepatch(t, tgt, false)
	if !strings.Contains(out, "P Makefile (text-patch)\n") || !strings.Contains(out, "P data.json (text-patch)\n") ||
		strings.Contains(out, "M ") {
		t.Fatalf("output:\n%s", out)
	}
	if got := readFile(t, tgt, "data.json"); got != local {
		t.Fatalf("text-adopted file modified: %q", got)
	}
	if !exists(t, tgt, ".git-scaffold/patches/data.json.patch") || !exists(t, tgt, ".git-scaffold/patches/Makefile.patch") {
		t.Fatal("text patch files missing")
	}
	mustCheckClean(t, tgt)
}

func TestRepatchUnparsableFallsBackToText(t *testing.T) {
	setEnv(t)
	_, tgt := initializedR(t)
	local := "{ not json\n"
	writeFiles(t, tgt, map[string]string{"config.json": local})
	out := mustRepatch(t, tgt, false)
	if !strings.Contains(out, "P config.json (text-patch)\n") {
		t.Fatalf("output:\n%s", out)
	}
	if got := readFile(t, tgt, "config.json"); got != local {
		t.Fatalf("file modified: %q", got)
	}
	if exists(t, tgt, ".git-scaffold/patches/config.json.json") {
		t.Fatal("unexpected json patch file")
	}
	mustCheckClean(t, tgt)
}

func TestRepatchFormattingOnlyChange(t *testing.T) {
	setEnv(t)
	_, tgt := initializedR(t)
	writeFiles(t, tgt, map[string]string{"config.json": `{"list":[1,2],"timeout":5,"name":"orders"}`})
	out := mustRepatch(t, tgt, false)
	if !strings.Contains(out, "M config.json\n") || strings.Contains(out, "P ") {
		t.Fatalf("output:\n%s", out)
	}
	if got := readFile(t, tgt, "config.json"); got != strings.ReplaceAll(baseConfigJSON, "@@NAME@@", "orders") {
		t.Fatalf("config.json = %q", got)
	}
	if cfg := parseTargetConfig(t, tgt); len(cfg.Overrides) != 0 {
		t.Fatalf("overrides = %+v", cfg.Overrides)
	}
	mustCheckClean(t, tgt)
}

func TestRepatchRevertDropsOverrideAndPreservesComments(t *testing.T) {
	setEnv(t)
	_, tgt := initializedR(t)
	writeFiles(t, tgt, map[string]string{"Makefile": baseMakefile + "# local\n"})
	mustRepatch(t, tgt, false)
	// Decorate the config with comments outside the overrides section.
	cfg := readFile(t, tgt, ".git-scaffold/config.toml")
	cfg = "# top comment\n" + strings.Replace(cfg, "[source]", "# source comment\n[source]", 1)
	writeFiles(t, tgt, map[string]string{".git-scaffold/config.toml": cfg})
	mustCheckClean(t, tgt)

	writeFiles(t, tgt, map[string]string{"Makefile": baseMakefile})
	out := mustRepatch(t, tgt, false)
	if !strings.Contains(out, "U Makefile\n") || !strings.Contains(out, "D .git-scaffold/patches/Makefile.patch\n") {
		t.Fatalf("output:\n%s", out)
	}
	if exists(t, tgt, ".git-scaffold/patches/Makefile.patch") {
		t.Fatal("stale patch file not deleted")
	}
	got := readFile(t, tgt, ".git-scaffold/config.toml")
	if !strings.Contains(got, "# top comment\n") || !strings.Contains(got, "# source comment\n[source]") ||
		strings.Contains(got, "overrides") {
		t.Fatalf("config.toml:\n%s", got)
	}
	mustCheckClean(t, tgt)
}

func TestRepatchAlreadyUpToDate(t *testing.T) {
	setEnv(t)
	_, tgt := initializedR(t)
	// Nothing differs at all.
	before := snapshot(t, tgt)
	out := mustRepatch(t, tgt, false)
	if !strings.Contains(out, "patches already up to date") {
		t.Fatalf("output:\n%s", out)
	}
	sameSnapshot(t, before, snapshot(t, tgt))

	// A second run after a real repatch is idempotent too.
	writeFiles(t, tgt, map[string]string{
		"config.json": `{"name":"orders","timeout":7,"list":[1,2]}`,
		"Makefile":    baseMakefile + "# local\n",
	})
	mustRepatch(t, tgt, false)
	before = snapshot(t, tgt)
	out = mustRepatch(t, tgt, false)
	if !strings.Contains(out, "patches already up to date") {
		t.Fatalf("output:\n%s", out)
	}
	sameSnapshot(t, before, snapshot(t, tgt))
	mustCheckClean(t, tgt)
}

func TestRepatchMissingManagedFile(t *testing.T) {
	setEnv(t)
	_, tgt := initializedR(t)
	if err := os.Remove(filepath.Join(tgt, "Makefile")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(tgt, "data.json")); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, tgt, map[string]string{"config.json": `{"name":"orders","timeout":7,"list":[1,2]}`})
	before := snapshot(t, tgt)
	err := Repatch(tgt, io.Discard, false)
	if err == nil || !strings.Contains(err.Error(), "cannot repatch missing managed files") ||
		!strings.Contains(err.Error(), "\n  Makefile") || !strings.Contains(err.Error(), "\n  data.json") {
		t.Fatalf("err = %v", err)
	}
	sameSnapshot(t, before, snapshot(t, tgt))
}

func TestRepatchBinaryRefused(t *testing.T) {
	setEnv(t)
	_, tgt := initializedR(t)
	writeFiles(t, tgt, map[string]string{"Makefile": "build:\x00\n"})
	before := snapshot(t, tgt)
	err := Repatch(tgt, io.Discard, false)
	if err == nil || !strings.Contains(err.Error(), "cannot repatch binary files") ||
		!strings.Contains(err.Error(), "Makefile") {
		t.Fatalf("err = %v", err)
	}
	sameSnapshot(t, before, snapshot(t, tgt))
}

func TestRepatchNoLock(t *testing.T) {
	setEnv(t)
	_, tgt := initializedR(t)
	if err := os.Remove(filepath.Join(tgt, ".git-scaffold", "lock")); err != nil {
		t.Fatal(err)
	}
	err := Repatch(tgt, io.Discard, false)
	if err == nil || !strings.Contains(err.Error(), "no .git-scaffold/lock; run `git scaffold apply` first") {
		t.Fatalf("err = %v", err)
	}
}

func TestRepatchTextOnly(t *testing.T) {
	setEnv(t)
	_, tgt := initializedR(t)
	local := `{"name":"orders","timeout":10,"list":[1,2]}`
	writeFiles(t, tgt, map[string]string{"config.json": local})
	out := mustRepatch(t, tgt, true)
	if !strings.Contains(out, "P config.json (text-patch)\n") || strings.Contains(out, "M ") {
		t.Fatalf("output:\n%s", out)
	}
	if got := readFile(t, tgt, "config.json"); got != local {
		t.Fatalf("file modified: %q", got)
	}
	if exists(t, tgt, ".git-scaffold/patches/config.json.json") {
		t.Fatal("unexpected json patch file")
	}
	mustCheckClean(t, tgt)

	// A check-clean tree is left alone even though json-patch would now be
	// preferred.
	if out := mustRepatch(t, tgt, false); !strings.Contains(out, "patches already up to date") {
		t.Fatalf("output:\n%s", out)
	}
	// A further hand edit re-derives the file: the text patch is replaced
	// by a json patch and the stale file deleted.
	writeFiles(t, tgt, map[string]string{"config.json": `{"name":"orders","timeout":11,"list":[1,2]}`})
	out = mustRepatch(t, tgt, false)
	if !strings.Contains(out, "P config.json (json-patch)\n") ||
		!strings.Contains(out, "D .git-scaffold/patches/config.json.patch\n") {
		t.Fatalf("output:\n%s", out)
	}
	if exists(t, tgt, ".git-scaffold/patches/config.json.patch") {
		t.Fatal("stale text patch not deleted")
	}
	mustCheckClean(t, tgt)
}

func TestRepatchCollapsesMultiplePatches(t *testing.T) {
	setEnv(t)
	_, tgt := initializedR(t)
	// Two hand-written json patches against config.json; the lock stays.
	writeFiles(t, tgt, map[string]string{
		".git-scaffold/config.toml": readFile(t, tgt, ".git-scaffold/config.toml") + `
[overrides."config.json"]
strategy = "json-patch"
patches = ["patches/one.json", "patches/two.json"]
`,
		".git-scaffold/patches/one.json": `[{"op":"replace","path":"/timeout","value":7}]`,
		".git-scaffold/patches/two.json": `[{"op":"add","path":"/list/2","value":3}]`,
	})
	if err := Apply(tgt, io.Discard, true); err != nil {
		t.Fatal(err)
	}
	mustCheckClean(t, tgt)
	lock := readFile(t, tgt, ".git-scaffold/lock")

	// Further hand edit, then repatch: one patch, at the mangled name.
	writeFiles(t, tgt, map[string]string{"config.json": `{"name":"orders","timeout":8,"list":[1,2,3]}`})
	out := mustRepatch(t, tgt, false)
	if !strings.Contains(out, "P config.json (json-patch)\n") ||
		!strings.Contains(out, "D .git-scaffold/patches/one.json\n") ||
		!strings.Contains(out, "D .git-scaffold/patches/two.json\n") {
		t.Fatalf("output:\n%s", out)
	}
	if exists(t, tgt, ".git-scaffold/patches/one.json") || exists(t, tgt, ".git-scaffold/patches/two.json") {
		t.Fatal("stale patch files not deleted")
	}
	cfg := parseTargetConfig(t, tgt)
	want := config.Override{Strategy: "json-patch", Patches: []string{"patches/config.json.json"}}
	if !reflect.DeepEqual(cfg.Overrides["config.json"], want) {
		t.Fatalf("overrides = %+v", cfg.Overrides)
	}
	if got := readFile(t, tgt, ".git-scaffold/lock"); got != lock {
		t.Fatalf("lock changed: %q", got)
	}
	mustCheckClean(t, tgt)
}

func TestRepatchReusesExistingPatchPath(t *testing.T) {
	setEnv(t)
	_, tgt := initializedR(t)
	writeFiles(t, tgt, map[string]string{
		".git-scaffold/config.toml": readFile(t, tgt, ".git-scaffold/config.toml") + `
[overrides."Makefile"]
strategy = "text-patch"
patches = ["patches/custom-name.patch"]
`,
		".git-scaffold/patches/custom-name.patch": unifiedDiff("Makefile", []byte(baseMakefile), []byte(baseMakefile+"# one\n"), true),
	})
	if err := Apply(tgt, io.Discard, true); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, tgt, map[string]string{"Makefile": baseMakefile + "# two\n"})
	out := mustRepatch(t, tgt, false)
	if !strings.Contains(out, "P Makefile (text-patch)\n") || strings.Contains(out, "D ") {
		t.Fatalf("output:\n%s", out)
	}
	if got := readFile(t, tgt, ".git-scaffold/patches/custom-name.patch"); !strings.Contains(got, "+# two") {
		t.Fatalf("patch not rewritten in place:\n%s", got)
	}
	if exists(t, tgt, ".git-scaffold/patches/Makefile.patch") {
		t.Fatal("new patch file created despite reusable path")
	}
	cfg := parseTargetConfig(t, tgt)
	if got := cfg.Overrides["Makefile"].Patches; !reflect.DeepEqual(got, []string{"patches/custom-name.patch"}) {
		t.Fatalf("patches = %v", got)
	}
	mustCheckClean(t, tgt)
}

func TestRepatchAvoidsPatchPathCollision(t *testing.T) {
	setEnv(t)
	_, tgt := initializedR(t)
	// Another override already refers to the path the mangling would pick
	// for Makefile.
	writeFiles(t, tgt, map[string]string{
		".git-scaffold/config.toml": readFile(t, tgt, ".git-scaffold/config.toml") + `
[overrides."data.json"]
strategy = "text-patch"
patches = ["patches/Makefile.patch"]
`,
		".git-scaffold/patches/Makefile.patch": unifiedDiff("data.json", []byte(baseDataJSON), []byte("{\n  \"x\": 2\n}\n"), true),
	})
	if err := Apply(tgt, io.Discard, true); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, tgt, map[string]string{"Makefile": baseMakefile + "# local\n"})
	mustRepatch(t, tgt, false)
	cfg := parseTargetConfig(t, tgt)
	if got := cfg.Overrides["Makefile"].Patches; !reflect.DeepEqual(got, []string{"patches/Makefile-2.patch"}) {
		t.Fatalf("patches = %v", got)
	}
	if got := cfg.Overrides["data.json"].Patches; !reflect.DeepEqual(got, []string{"patches/Makefile.patch"}) {
		t.Fatalf("other override disturbed: %v", got)
	}
	mustCheckClean(t, tgt)
}

func TestRepatchDropsOverrideOfUnmanagedPath(t *testing.T) {
	setEnv(t)
	_, tgt := initializedR(t)
	writeFiles(t, tgt, map[string]string{
		".git-scaffold/config.toml": readFile(t, tgt, ".git-scaffold/config.toml") + `
[overrides."nope.txt"]
strategy = "text-patch"
patches = ["patches/nope.patch"]
`,
		".git-scaffold/patches/nope.patch": "garbage\n",
	})
	var out bytes.Buffer
	if _, err := Check(tgt, &out); err == nil {
		t.Fatal("check should fail with an override of an unmanaged file")
	}
	got := mustRepatch(t, tgt, false)
	if !strings.Contains(got, "U nope.txt\n") || !strings.Contains(got, "D .git-scaffold/patches/nope.patch\n") {
		t.Fatalf("output:\n%s", got)
	}
	mustCheckClean(t, tgt)
}

// --- init --existing strategy preference ---

func TestInitExistingPrefersJSONPatch(t *testing.T) {
	setEnv(t)
	src := sourceRepoR(t)
	tgt := newRepo(t)
	writeFiles(t, tgt, map[string]string{
		"config.json":   `{"name":"orders","timeout":10,"list":[1,2]}`,
		"settings.yaml": "run:\n  timeout: 9m\nlinters:\n- govet\n",
		"data.json":     `{"x": 2}`,
		"Makefile":      baseMakefile + "# local\n",
	})
	var out bytes.Buffer
	if err := Init(tgt, &out, filepath.ToSlash(src), "main", repatchArgs, true, false, false); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"P config.json (json-patch)\n", "M config.json\n",
		"P settings.yaml (json-patch)\n",
		"P data.json (text-patch)\n", "P Makefile (text-patch)\n",
		"adopted 4 existing files as patches",
	} {
		if !strings.Contains(out.String(), line) {
			t.Fatalf("output lacks %q:\n%s", line, out.String())
		}
	}
	if strings.Contains(out.String(), "M data.json") || strings.Contains(out.String(), "M Makefile") {
		t.Fatalf("text-adopted files reported as modified:\n%s", out.String())
	}
	// json-patched file normalized to the canonical serialization; the
	// text-adopted file untouched.
	if got := readFile(t, tgt, "config.json"); !strings.Contains(got, "\n  \"timeout\": 10") {
		t.Fatalf("config.json not normalized: %q", got)
	}
	if got := readFile(t, tgt, "data.json"); got != `{"x": 2}` {
		t.Fatalf("data.json modified: %q", got)
	}
	cfg := parseTargetConfig(t, tgt)
	if cfg.Overrides["config.json"].Strategy != "json-patch" || cfg.Overrides["data.json"].Strategy != "text-patch" {
		t.Fatalf("overrides = %+v", cfg.Overrides)
	}
	if !exists(t, tgt, ".git-scaffold/patches/config.json.json") || !exists(t, tgt, ".git-scaffold/patches/settings.yaml.json") {
		t.Fatal("json patch files missing")
	}
	mustCheckClean(t, tgt)
}

func TestInitExistingFormattingOnlyNormalizes(t *testing.T) {
	setEnv(t)
	src := sourceRepoR(t)
	tgt := newRepo(t)
	writeFiles(t, tgt, map[string]string{"config.json": `{"list":[1,2],"timeout":5,"name":"orders"}`})
	var out bytes.Buffer
	if err := Init(tgt, &out, filepath.ToSlash(src), "main", repatchArgs, true, false, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "M config.json\n") || strings.Contains(out.String(), "adopted") {
		t.Fatalf("output:\n%s", out.String())
	}
	if cfg := parseTargetConfig(t, tgt); len(cfg.Overrides) != 0 {
		t.Fatalf("overrides = %+v", cfg.Overrides)
	}
	mustCheckClean(t, tgt)
}

func TestInitExistingTextOnly(t *testing.T) {
	setEnv(t)
	src := sourceRepoR(t)
	tgt := newRepo(t)
	local := `{"name":"orders","timeout":10,"list":[1,2]}`
	writeFiles(t, tgt, map[string]string{"config.json": local})
	var out bytes.Buffer
	if err := Init(tgt, &out, filepath.ToSlash(src), "main", repatchArgs, true, false, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "P config.json (text-patch)\n") || strings.Contains(out.String(), "M ") {
		t.Fatalf("output:\n%s", out.String())
	}
	if got := readFile(t, tgt, "config.json"); got != local {
		t.Fatalf("file modified: %q", got)
	}
	mustCheckClean(t, tgt)
}

// --- rewriteOverrides ---

func TestRewriteOverrides(t *testing.T) {
	const original = `# leading comment
[scaffold]
version = 1

[source]  # trailing comment
git = "x.git"
ref = "main"

[overrides."Makefile"]
strategy = "text-patch"
patches = ["patches/Makefile.patch"]

[args]
name = "orders"   # keep me

[ overrides."old.json" ]
strategy = "json-patch"
patches = [
    "patches/old.json",
]

[propose]
create-command = ["echo", "hi"]
`
	ov := map[string]config.Override{
		"config.json": {Strategy: "json-patch", Patches: []string{"patches/config.json.json"}},
		"Makefile":    {Strategy: "text-patch", Patches: []string{"patches/Makefile.patch"}},
	}
	got, err := rewriteOverrides([]byte(original), ov)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	for _, want := range []string{"# leading comment\n", "[source]  # trailing comment\n", "# keep me\n", "[propose]\n"} {
		if !strings.Contains(s, want) {
			t.Fatalf("lost %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "old.json") || strings.Contains(s, "\n\n\n") {
		t.Fatalf("stale overrides or blank runs:\n%s", s)
	}
	cfg, err := config.ParseTarget(got)
	if err != nil {
		t.Fatalf("%v\n%s", err, s)
	}
	if !reflect.DeepEqual(cfg.Overrides, ov) || cfg.Args["name"] != "orders" || cfg.SourceRef != "main" ||
		!reflect.DeepEqual(cfg.ProposeCreateCommand, []string{"echo", "hi"}) {
		t.Fatalf("parsed = %+v", cfg)
	}

	// Removing all overrides leaves no overrides section at all, and the
	// result is idempotent.
	none, err := rewriteOverrides(got, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(none), "overrides") || !strings.HasSuffix(string(none), "[propose]\ncreate-command = [\"echo\", \"hi\"]\n") {
		t.Fatalf("result:\n%s", none)
	}
	again, err := rewriteOverrides(got, ov)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != s {
		t.Fatalf("not idempotent:\n%s\n---\n%s", s, again)
	}
}

func TestRewriteOverridesInlineTableFallback(t *testing.T) {
	// An inline table cannot be edited line-wise, so the whole file is
	// re-encoded from the parsed configuration.
	const src = `overrides = { "Makefile" = { strategy = "text-patch", patches = ["patches/a.patch"] } }
[scaffold]
version = 1
[source]
git = "x.git"
[args]
name = "orders"
`
	ov := map[string]config.Override{"data.json": {Strategy: "text-patch", Patches: []string{"patches/b.patch"}}}
	got, err := rewriteOverrides([]byte(src), ov)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.ParseTarget(got)
	if err != nil {
		t.Fatalf("%v\n%s", err, got)
	}
	if !reflect.DeepEqual(cfg.Overrides, ov) || cfg.Args["name"] != "orders" || cfg.SourceGit != "x.git" {
		t.Fatalf("parsed = %+v\n%s", cfg, got)
	}
	if strings.Contains(string(got), "a.patch") {
		t.Fatalf("old inline override survived:\n%s", got)
	}
}

func TestRewriteOverridesInvalidOriginal(t *testing.T) {
	_, err := rewriteOverrides([]byte("not toml ["), nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestFreePatchPath(t *testing.T) {
	used := map[string]bool{"patches/a--b.patch": true}
	if got := freePatchPath("a/b", "text-patch", used); got != "patches/a--b-2.patch" {
		t.Fatalf("got %q", got)
	}
	if got := freePatchPath("a/b", "json-patch", used); got != "patches/a--b.json" {
		t.Fatalf("got %q", got)
	}
	if got := freePatchPath("a/b", "text-patch", used); got != "patches/a--b-3.patch" {
		t.Fatalf("got %q", got)
	}
}

// --- adversarial-review regressions ---

func TestRepatchLeavesCheckCleanOverridesAlone(t *testing.T) {
	setEnv(t)
	src := sourceRepoR(t)
	tgt := newRepo(t)
	// Text patches on json-permitted files, established by --text-patch.
	writeFiles(t, tgt, map[string]string{
		"config.json":   `{"name":"orders","timeout":10,"list":[1,2]}`,
		"settings.yaml": "run:\n  timeout: 9m\nlinters:\n- govet\n",
	})
	if err := Init(tgt, io.Discard, filepath.ToSlash(src), "main", repatchArgs, true, false, true); err != nil {
		t.Fatal(err)
	}
	mustCheckClean(t, tgt)
	before := snapshot(t, tgt)
	out := mustRepatch(t, tgt, false)
	if !strings.Contains(out, "patches already up to date") {
		t.Fatalf("output:\n%s", out)
	}
	sameSnapshot(t, before, snapshot(t, tgt))
}

func TestRepatchRederivesWhenCurrentMaterializationBroken(t *testing.T) {
	setEnv(t)
	_, tgt := initializedR(t)
	writeFiles(t, tgt, map[string]string{"Makefile": baseMakefile + "# local\n"})
	mustRepatch(t, tgt, false)
	mustCheckClean(t, tgt)
	// Corrupt the patch file: check now fails, repatch regenerates it.
	writeFiles(t, tgt, map[string]string{".git-scaffold/patches/Makefile.patch": "garbage\n"})
	if _, err := Check(tgt, io.Discard); err == nil {
		t.Fatal("check should fail with a broken patch")
	}
	out := mustRepatch(t, tgt, false)
	if !strings.Contains(out, "P Makefile (text-patch)\n") {
		t.Fatalf("output:\n%s", out)
	}
	if got := readFile(t, tgt, ".git-scaffold/patches/Makefile.patch"); !strings.Contains(got, "+# local") {
		t.Fatalf("patch:\n%s", got)
	}
	mustCheckClean(t, tgt)
}

func TestRepatchSharedPatchPathNotRewrittenInPlace(t *testing.T) {
	setEnv(t)
	_, tgt := initializedR(t)
	shared := unifiedDiff("x", []byte("{\n  \"x\": 1\n}\n"), []byte("{\n  \"x\": 2\n}\n"), true)
	writeFiles(t, tgt, map[string]string{
		".git-scaffold/config.toml": readFile(t, tgt, ".git-scaffold/config.toml") + `
[overrides."data.json"]
strategy = "text-patch"
patches = ["patches/shared.patch"]

[overrides."config.json"]
strategy = "text-patch"
patches = ["patches/shared.patch"]
`,
		".git-scaffold/patches/shared.patch": shared,
	})
	// The shared patch applies to data.json only; config.json is broken,
	// so the current materialization fails and everything is re-derived.
	writeFiles(t, tgt, map[string]string{"data.json": "{\n  \"x\": 2\n}\n"})
	mustRepatch(t, tgt, true)
	cfg := parseTargetConfig(t, tgt)
	if got := cfg.Overrides["data.json"].Patches; !reflect.DeepEqual(got, []string{"patches/data.json.patch"}) {
		t.Fatalf("data.json patches = %v", got)
	}
	if _, ok := cfg.Overrides["config.json"]; ok {
		t.Fatalf("config.json (at base) kept an override: %+v", cfg.Overrides)
	}
	if exists(t, tgt, ".git-scaffold/patches/shared.patch") {
		t.Fatal("stale shared patch not deleted")
	}
	mustCheckClean(t, tgt)
}

func TestRepatchMultiDocumentYAMLUsesText(t *testing.T) {
	setEnv(t)
	_, tgt := initializedR(t)
	local := "linters:\n- govet\nrun:\n  timeout: 5m\n---\napiVersion: v1\nkind: Service\n"
	writeFiles(t, tgt, map[string]string{"settings.yaml": local})
	out := mustRepatch(t, tgt, false)
	if !strings.Contains(out, "P settings.yaml (text-patch)\n") || strings.Contains(out, "M ") {
		t.Fatalf("output:\n%s", out)
	}
	if got := readFile(t, tgt, "settings.yaml"); got != local {
		t.Fatalf("second document lost: %q", got)
	}
	mustCheckClean(t, tgt)
}

func TestRepatchNonCanonicalYAMLBaseUsesText(t *testing.T) {
	setEnv(t)
	src := newRepo(t)
	base := "on: push\njobs:\n  build:\n    go-version: 1.20\n    mode: 0755\n    answer: yes\n"
	writeFiles(t, src, map[string]string{
		".git-scaffold/config.toml": "[scaffold]\nversion = 1\n\n[[files]]\npath = \"ci.yml\"\npatch = \"json-patch\"\n",
		"ci.yml":                    base,
	})
	commitAll(t, src, "A")
	tgt := newRepo(t)
	if err := Init(tgt, io.Discard, filepath.ToSlash(src), "main", nil, false, false, false); err != nil {
		t.Fatal(err)
	}
	local := base + "    extra: true\n"
	writeFiles(t, tgt, map[string]string{"ci.yml": local})
	out := mustRepatch(t, tgt, false)
	if !strings.Contains(out, "P ci.yml (text-patch)\n") || strings.Contains(out, "M ") {
		t.Fatalf("output:\n%s", out)
	}
	if got := readFile(t, tgt, "ci.yml"); got != local {
		t.Fatalf("untouched keys rewritten: %q", got)
	}
	mustCheckClean(t, tgt)
}

func TestRepatchDuplicateJSONKeysUseText(t *testing.T) {
	setEnv(t)
	_, tgt := initializedR(t)
	local := `{"name":"orders","timeout":5,"timeout":6,"list":[1,2]}`
	writeFiles(t, tgt, map[string]string{"config.json": local})
	out := mustRepatch(t, tgt, false)
	if !strings.Contains(out, "P config.json (text-patch)\n") {
		t.Fatalf("output:\n%s", out)
	}
	if got := readFile(t, tgt, "config.json"); got != local {
		t.Fatalf("file modified: %q", got)
	}
	mustCheckClean(t, tgt)
}

func TestRepatchEmptyYAMLUsesText(t *testing.T) {
	setEnv(t)
	_, tgt := initializedR(t)
	writeFiles(t, tgt, map[string]string{"settings.yaml": "# nothing here\n"})
	out := mustRepatch(t, tgt, false)
	if !strings.Contains(out, "P settings.yaml (text-patch)\n") {
		t.Fatalf("output:\n%s", out)
	}
	if got := readFile(t, tgt, "settings.yaml"); got != "# nothing here\n" {
		t.Fatalf("file modified: %q", got)
	}
	mustCheckClean(t, tgt)
}

func TestRepatchUnpatchedLinesSorted(t *testing.T) {
	setEnv(t)
	_, tgt := initializedR(t)
	writeFiles(t, tgt, map[string]string{"data.json": `{"x": 2}`})
	mustRepatch(t, tgt, false)
	writeFiles(t, tgt, map[string]string{
		".git-scaffold/config.toml": readFile(t, tgt, ".git-scaffold/config.toml") + `
[overrides."aaa.txt"]
strategy = "text-patch"
patches = ["patches/aaa.patch"]
`,
		".git-scaffold/patches/aaa.patch": "garbage\n",
		"data.json":                       baseDataJSON,
	})
	out := mustRepatch(t, tgt, false)
	if strings.Index(out, "U aaa.txt\n") > strings.Index(out, "U data.json\n") || !strings.Contains(out, "U aaa.txt\n") {
		t.Fatalf("U lines not in path order:\n%s", out)
	}
}

func TestRewriteOverridesCRLF(t *testing.T) {
	original := "# c\r\n[scaffold]\r\nversion = 1\r\n\r\n[source]\r\ngit = \"x.git\"\r\n\r\n" +
		"[overrides.\"old\"]\r\nstrategy = \"text-patch\"\r\npatches = [\"patches/old.patch\"]\r\n"
	ov := map[string]config.Override{"Makefile": {Strategy: "text-patch", Patches: []string{"patches/Makefile.patch"}}}
	got, err := rewriteOverrides([]byte(original), ov)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if strings.Contains(strings.ReplaceAll(s, "\r\n", ""), "\n") || !strings.HasPrefix(s, "# c\r\n") ||
		strings.Contains(s, "old.patch") || !strings.Contains(s, "Makefile.patch") {
		t.Fatalf("result:\n%q", s)
	}
	cfg, err := config.ParseTarget(got)
	if err != nil || !reflect.DeepEqual(cfg.Overrides, ov) {
		t.Fatalf("parsed = %+v, %v", cfg, err)
	}
}
