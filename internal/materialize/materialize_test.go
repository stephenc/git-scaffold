package materialize

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/stephenc/git-scaffold/internal/config"
)

func mustDescriptor(t *testing.T, s string) *config.Descriptor {
	t.Helper()
	d, err := config.ParseDescriptor([]byte("[scaffold]\nversion = 1\n" + s))
	if err != nil {
		t.Fatalf("ParseDescriptor: %v", err)
	}
	return d
}

func mustTarget(t *testing.T, s string) *config.TargetConfig {
	t.Helper()
	c, err := config.ParseTarget([]byte("[scaffold]\nversion = 1\n[source]\ngit = \"x.git\"\n" + s))
	if err != nil {
		t.Fatalf("ParseTarget: %v", err)
	}
	return c
}

func tree(pairs ...string) map[string][]byte {
	m := map[string][]byte{}
	for i := 0; i < len(pairs); i += 2 {
		m[pairs[i]] = []byte(pairs[i+1])
	}
	return m
}

func wantErr(t *testing.T, err error, substr string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), substr) {
		t.Fatalf("error = %v, want containing %q", err, substr)
	}
}

// yamlEqual compares two YAML documents structurally: serialization details
// (key order, list indent) are not part of the json-patch contract.
func yamlEqual(t *testing.T, got []byte, want string) {
	t.Helper()
	g, err := yaml.YAMLToJSON(got)
	if err != nil {
		t.Fatalf("got is not YAML: %v\n%s", err, got)
	}
	w, err := yaml.YAMLToJSON([]byte(want))
	if err != nil {
		t.Fatalf("want is not YAML: %v", err)
	}
	if !bytes.Equal(g, w) {
		t.Errorf("YAML mismatch:\ngot  %s\nwant %s", g, w)
	}
}

// --- Managed files / globs ---

func TestExactPathMaterialized(t *testing.T) {
	d := mustDescriptor(t, "[[files]]\npath = \"Makefile\"\n")
	got, err := Materialize(tree("Makefile", "all:\n", "ignored.txt", "x"), d, mustTarget(t, ""), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]byte{"Makefile": []byte("all:\n")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestMissingExactPath(t *testing.T) {
	d := mustDescriptor(t, "[[files]]\npath = \"Makefile\"\n")
	_, err := Materialize(tree("other", "x"), d, mustTarget(t, ""), nil)
	wantErr(t, err, `managed file "Makefile" does not exist`)
}

func TestMissingExactPathAllowEmptyDoesNotSuppress(t *testing.T) {
	// §9.3: allow-empty must not suppress a missing exact path.
	d := mustDescriptor(t, "[[files]]\npath = \"Makefile\"\nallow-empty = true\n")
	_, err := Materialize(tree("other", "x"), d, mustTarget(t, ""), nil)
	wantErr(t, err, `managed file "Makefile" does not exist`)
}

func TestGlobExpansion(t *testing.T) {
	d := mustDescriptor(t, "[[files]]\npath = \".github/workflows/*.yml\"\n")
	src := tree(
		".github/workflows/ci.yml", "ci",
		".github/workflows/test.yml", "test",
		".github/workflows/nested/deep.yml", "deep",
		"README.md", "readme",
	)
	got, err := Materialize(src, d, mustTarget(t, ""), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]byte{
		".github/workflows/ci.yml":   []byte("ci"),
		".github/workflows/test.yml": []byte("test"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestGlobZeroMatchesError(t *testing.T) {
	d := mustDescriptor(t, "[[files]]\npath = \"docs/**/*.md\"\n")
	_, err := Materialize(tree("README.md", "x"), d, mustTarget(t, ""), nil)
	wantErr(t, err, `pattern "docs/**/*.md" matches no files`)
}

func TestGlobZeroMatchesAllowEmpty(t *testing.T) {
	d := mustDescriptor(t, "[[files]]\npath = \"docs/**/*.md\"\nallow-empty = true\n")
	got, err := Materialize(tree("README.md", "x"), d, mustTarget(t, ""), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

func TestOverlappingIdenticalRules(t *testing.T) {
	d := mustDescriptor(t, `
[[files]]
path = "config/*.yml"

[[files]]
path = "config/app.yml"
`)
	got, err := Materialize(tree("config/app.yml", "x"), d, mustTarget(t, ""), nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(got["config/app.yml"]) != "x" {
		t.Errorf("got %v", got)
	}
}

func TestOverlappingBehaviorallyIdenticalRules(t *testing.T) {
	// An explicit per-rule token equal to the global token changes nothing:
	// effective configuration is identical, so the overlap is allowed (§9.5).
	d := mustDescriptor(t, `
[[arguments]]
name = "a"
token = "@@A@@"

[[files]]
path = "config/*.yml"

[[files]]
path = "config/app.yml"

[files.arguments.a]
token = "@@A@@"
`)
	got, err := Materialize(tree("config/app.yml", "v: @@A@@\n"), d, mustTarget(t, "[args]\na = \"1\"\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(got["config/app.yml"]) != "v: 1\n" {
		t.Errorf("got %q", got["config/app.yml"])
	}
}

func TestOverlappingConflictingGlobRules(t *testing.T) {
	d := mustDescriptor(t, `
[[files]]
path = "config/*.yml"

[[files]]
path = "config/app.*"
substitute = false
`)
	_, err := Materialize(tree("config/app.yml", "x"), d, mustTarget(t, ""), nil)
	wantErr(t, err, "ambiguous descriptor")
}

func TestOverlappingConflictingGlobPatchRules(t *testing.T) {
	d := mustDescriptor(t, `
[[files]]
path = "config/*.yml"

[[files]]
path = "config/app.*"
patch = "json-patch"
`)
	_, err := Materialize(tree("config/app.yml", "x"), d, mustTarget(t, ""), nil)
	wantErr(t, err, "ambiguous descriptor")
}

func TestExactRuleTrumpsOverlappingGlob(t *testing.T) {
	// §9.5: an exact rule is more specific than a glob, so it alone governs
	// its file even when the two imply different behavior.
	d := mustDescriptor(t, `
[[arguments]]
name = "a"
token = "@@A@@"

[[files]]
path = "config/*.yml"

[[files]]
path = "config/raw.yml"
substitute = false
`)
	got, err := Materialize(
		tree("config/app.yml", "v: @@A@@\n", "config/raw.yml", "v: @@A@@\n"),
		d, mustTarget(t, "[args]\na = \"1\"\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(got["config/app.yml"]) != "v: 1\n" {
		t.Errorf("glob-governed file: got %q", got["config/app.yml"])
	}
	if string(got["config/raw.yml"]) != "v: @@A@@\n" {
		t.Errorf("exact-governed file: got %q", got["config/raw.yml"])
	}
}

func TestConflictingGlobsWithExactCarveOut(t *testing.T) {
	// Two globs that disagree about a file are not ambiguous when an exact
	// rule governs that file: the "otherwise" of §9.5 never applies to it.
	d := mustDescriptor(t, `
[[files]]
path = "config/*.yml"

[[files]]
path = "config/app.*"
substitute = false

[[files]]
path = "config/app.yml"
substitute = false
`)
	if _, err := Materialize(tree("config/app.yml", "x"), d, mustTarget(t, ""), nil); err != nil {
		t.Fatal(err)
	}
}

func TestSourceChangesManagedSetThroughGlob(t *testing.T) {
	// The concrete managed set follows the source tree: a file added at the
	// new commit joins, a removed file leaves (§30).
	d := mustDescriptor(t, "[[files]]\npath = \"wf/*.yml\"\n")
	trg := mustTarget(t, "")

	old, err := Materialize(tree("wf/ci.yml", "ci", "wf/test.yml", "test"), d, trg, nil)
	if err != nil {
		t.Fatal(err)
	}
	now, err := Materialize(tree("wf/ci.yml", "ci", "wf/security.yml", "sec"), d, trg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := old["wf/test.yml"]; !ok {
		t.Error("old set missing wf/test.yml")
	}
	if _, ok := now["wf/test.yml"]; ok {
		t.Error("new set still contains wf/test.yml")
	}
	if _, ok := now["wf/security.yml"]; !ok {
		t.Error("new set missing wf/security.yml")
	}
}

func TestDescriptorGlobWidenedNarrowedRemoved(t *testing.T) {
	src := tree("a/x.yml", "x", "a/b/y.yml", "y", "README.md", "r")
	trg := mustTarget(t, "")

	narrow, err := Materialize(src, mustDescriptor(t, "[[files]]\npath = \"a/*.yml\"\n"), trg, nil)
	if err != nil {
		t.Fatal(err)
	}
	wide, err := Materialize(src, mustDescriptor(t, "[[files]]\npath = \"a/**/*.yml\"\n"), trg, nil)
	if err != nil {
		t.Fatal(err)
	}
	removed, err := Materialize(src, mustDescriptor(t, "[[files]]\npath = \"README.md\"\n"), trg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(narrow) != 1 || len(wide) != 2 {
		t.Errorf("narrow = %d files, wide = %d files", len(narrow), len(wide))
	}
	if _, ok := removed["a/x.yml"]; ok || len(removed) != 1 {
		t.Errorf("removed = %v", removed)
	}
}

// --- Arguments / substitution ---

const argDescriptor = `
[[arguments]]
name = "project_name"
token = "@@PROJECT_NAME@@"

[[arguments]]
name = "go_version"
default = "1.26"
token = "@@GO_VERSION@@"
`

func TestMissingRequiredArguments(t *testing.T) {
	d := mustDescriptor(t, argDescriptor+"\n[[arguments]]\nname = \"module\"\n[[files]]\npath = \"f\"\n")
	_, err := Materialize(tree("f", "x"), d, mustTarget(t, ""), nil)
	wantErr(t, err, "missing required arguments: module, project_name")
}

func TestDefaultResolutionAndTargetOverride(t *testing.T) {
	d := mustDescriptor(t, argDescriptor+"[[files]]\npath = \"f\"\n")
	src := tree("f", "p=@@PROJECT_NAME@@ go=@@GO_VERSION@@\n")

	got, err := Materialize(src, d, mustTarget(t, "[args]\nproject_name = \"orders\"\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(got["f"]) != "p=orders go=1.26\n" {
		t.Errorf("default: got %q", got["f"])
	}

	got, err = Materialize(src, d, mustTarget(t, "[args]\nproject_name = \"orders\"\ngo_version = \"1.27\"\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(got["f"]) != "p=orders go=1.27\n" {
		t.Errorf("override: got %q", got["f"])
	}
}

func TestUndeclaredTargetArgument(t *testing.T) {
	d := mustDescriptor(t, argDescriptor+"[[files]]\npath = \"f\"\n")
	trg := mustTarget(t, "[args]\nproject_name = \"x\"\nprojcet_name = \"typo\"\n")
	_, err := Materialize(tree("f", "x"), d, trg, nil)
	wantErr(t, err, `argument "projcet_name" is not declared`)
}

func TestTokenOccurrences(t *testing.T) {
	// Zero occurrences is not an error; multiple occurrences all replace (§19).
	d := mustDescriptor(t, argDescriptor+"[[files]]\npath = \"zero\"\n[[files]]\npath = \"many\"\n")
	src := tree("zero", "no tokens here\n", "many", "@@PROJECT_NAME@@/@@PROJECT_NAME@@ @@PROJECT_NAME@@\n")
	got, err := Materialize(src, d, mustTarget(t, "[args]\nproject_name = \"orders\"\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(got["zero"]) != "no tokens here\n" {
		t.Errorf("zero: got %q", got["zero"])
	}
	if string(got["many"]) != "orders/orders orders\n" {
		t.Errorf("many: got %q", got["many"])
	}
}

func TestPerRuleTokenSubstitution(t *testing.T) {
	d := mustDescriptor(t, `
[[arguments]]
name = "project_name"
token = "@@PROJECT_NAME@@"

[[files]]
path = "code.txt"

[[files]]
path = "docs/**/*.md"

[files.arguments.project_name]
token = "{{ project.name }}"
`)
	src := tree(
		"code.txt", "@@PROJECT_NAME@@ {{ project.name }}\n",
		"docs/a/guide.md", "@@PROJECT_NAME@@ {{ project.name }}\n",
	)
	got, err := Materialize(src, d, mustTarget(t, "[args]\nproject_name = \"orders\"\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Global token in the docs rule stays untouched; the rule token applies,
	// and vice versa — the override reaches files matched through the glob.
	if string(got["code.txt"]) != "orders {{ project.name }}\n" {
		t.Errorf("code.txt: got %q", got["code.txt"])
	}
	if string(got["docs/a/guide.md"]) != "@@PROJECT_NAME@@ orders\n" {
		t.Errorf("docs: got %q", got["docs/a/guide.md"])
	}
}

func TestPerFileArgumentDisable(t *testing.T) {
	d := mustDescriptor(t, argDescriptor+`
[[files]]
path = "examples/**/*.txt"

[files.arguments.project_name]
enabled = false
`)
	src := tree("examples/a.txt", "@@PROJECT_NAME@@ @@GO_VERSION@@\n")
	got, err := Materialize(src, d, mustTarget(t, "[args]\nproject_name = \"orders\"\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Disabled argument's token remains; other arguments still substitute (§15).
	if string(got["examples/a.txt"]) != "@@PROJECT_NAME@@ 1.26\n" {
		t.Errorf("got %q", got["examples/a.txt"])
	}
}

func TestSubstituteFalseThroughGlob(t *testing.T) {
	d := mustDescriptor(t, argDescriptor+`
[[files]]
path = "fixtures/**/*"
substitute = false
`)
	src := tree("fixtures/deep/a.txt", "@@PROJECT_NAME@@ @@GO_VERSION@@\n")
	got, err := Materialize(src, d, mustTarget(t, "[args]\nproject_name = \"orders\"\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(got["fixtures/deep/a.txt"]) != "@@PROJECT_NAME@@ @@GO_VERSION@@\n" {
		t.Errorf("got %q", got["fixtures/deep/a.txt"])
	}
}

func TestSimultaneousSubstitution(t *testing.T) {
	// §18: replacement output is never rescanned; order cannot matter.
	d := mustDescriptor(t, `
[[arguments]]
name = "a"
token = "@@A@@"

[[arguments]]
name = "b"
token = "@@B@@"

[[files]]
path = "f"
`)
	trg := mustTarget(t, "[args]\na = \"@@B@@\"\nb = \"hello\"\n")
	got, err := Materialize(tree("f", "@@A@@ @@B@@"), d, trg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(got["f"]) != "@@B@@ hello" {
		t.Errorf("got %q, want %q", got["f"], "@@B@@ hello")
	}
}

func TestLongestTokenWinsAtSamePosition(t *testing.T) {
	d := mustDescriptor(t, `
[[arguments]]
name = "a"
token = "@@A@@"

[[arguments]]
name = "ab"
token = "@@A@@B@@"

[[files]]
path = "f"
`)
	trg := mustTarget(t, "[args]\na = \"1\"\nab = \"2\"\n")
	got, err := Materialize(tree("f", "@@A@@B@@"), d, trg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(got["f"]) != "2" {
		t.Errorf("got %q, want %q", got["f"], "2")
	}
}

func TestDuplicateEffectiveTokensWithinFile(t *testing.T) {
	d := mustDescriptor(t, `
[[arguments]]
name = "a"
token = "@@X@@"

[[arguments]]
name = "b"

[[files]]
path = "f"

[files.arguments.b]
token = "@@X@@"
`)
	trg := mustTarget(t, "[args]\na = \"1\"\nb = \"2\"\n")
	_, err := Materialize(tree("f", "x"), d, trg, nil)
	wantErr(t, err, `resolve to the same effective token "@@X@@"`)
}

func TestSameTokenInDifferentFilesAllowed(t *testing.T) {
	// §19: reuse across files is fine when no single file is ambiguous.
	d := mustDescriptor(t, `
[[arguments]]
name = "a"

[[arguments]]
name = "b"

[[files]]
path = "f1"

[files.arguments.a]
token = "@@X@@"

[[files]]
path = "f2"

[files.arguments.b]
token = "@@X@@"
`)
	trg := mustTarget(t, "[args]\na = \"1\"\nb = \"2\"\n")
	got, err := Materialize(tree("f1", "@@X@@", "f2", "@@X@@"), d, trg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(got["f1"]) != "1" || string(got["f2"]) != "2" {
		t.Errorf("got %q %q", got["f1"], got["f2"])
	}
}

func TestBytePreservation(t *testing.T) {
	// §21: CRLF, missing final newline, and non-UTF-8 bytes pass through.
	d := mustDescriptor(t, argDescriptor+"[[files]]\npath = \"f\"\n")
	content := append([]byte("a\r\n@@PROJECT_NAME@@\r\n\xff\xfe raw"), 0x00)
	got, err := Materialize(map[string][]byte{"f": content}, d,
		mustTarget(t, "[args]\nproject_name = \"orders\"\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := append([]byte("a\r\norders\r\n\xff\xfe raw"), 0x00)
	if !bytes.Equal(got["f"], want) {
		t.Errorf("got %q, want %q", got["f"], want)
	}
}

// --- Patches ---

const patchableDescriptor = `
[[files]]
path = ".golangci.yml"
patch = "json-patch"

[[files]]
path = "plain.txt"
`

func TestUnauthorizedTargetPatch(t *testing.T) {
	// §23: structured strategies require the rule's patch declaration.
	d := mustDescriptor(t, patchableDescriptor)
	trg := mustTarget(t, "[overrides.\"plain.txt\"]\nstrategy = \"json-patch\"\npatches = [\"patches/x.json\"]\n")
	_, err := Materialize(tree(".golangci.yml", "a: 1\n", "plain.txt", "x"), d, trg, nil)
	wantErr(t, err, "plain.txt: the source does not permit structured patching")
}

func TestTextPatchAlwaysPermitted(t *testing.T) {
	// §23: text-patch is the universal escape hatch — accepted on a rule
	// without any patch declaration.
	d := mustDescriptor(t, patchableDescriptor)
	trg := mustTarget(t, "[overrides.\"plain.txt\"]\nstrategy = \"text-patch\"\npatches = [\"patches/x.diff\"]\n")
	patches := map[string][]byte{
		"patches/x.diff": []byte("--- a/plain.txt\n+++ b/plain.txt\n@@ -1 +1 @@\n-x\n\\ No newline at end of file\n+y\n\\ No newline at end of file\n"),
	}
	got, err := Materialize(tree(".golangci.yml", "a: 1\n", "plain.txt", "x"), d, trg, patches)
	if err != nil {
		t.Fatal(err)
	}
	if string(got["plain.txt"]) != "y" {
		t.Errorf("got %q", got["plain.txt"])
	}
}

func TestOverrideOfUnmanagedFile(t *testing.T) {
	d := mustDescriptor(t, patchableDescriptor)
	trg := mustTarget(t, "[overrides.\"other.yml\"]\nstrategy = \"json-patch\"\npatches = [\"patches/x.json\"]\n")
	_, err := Materialize(tree(".golangci.yml", "a: 1\n", "plain.txt", "x"), d, trg, nil)
	wantErr(t, err, "other.yml: override targets a file the source does not manage")
}

func TestTextPatchOnStructuredRulePermitted(t *testing.T) {
	// §23: text-patch remains available even where the rule permits a
	// structured strategy.
	d := mustDescriptor(t, patchableDescriptor)
	trg := mustTarget(t, "[overrides.\".golangci.yml\"]\nstrategy = \"text-patch\"\npatches = [\"patches/x.diff\"]\n")
	patches := map[string][]byte{
		"patches/x.diff": []byte("--- a/.golangci.yml\n+++ b/.golangci.yml\n@@ -1 +1 @@\n-a: 1\n+a: 2\n"),
	}
	got, err := Materialize(tree(".golangci.yml", "a: 1\n", "plain.txt", "x"), d, trg, patches)
	if err != nil {
		t.Fatal(err)
	}
	if string(got[".golangci.yml"]) != "a: 2\n" {
		t.Errorf("got %q", got[".golangci.yml"])
	}
}

func TestOverrideStrategyMismatch(t *testing.T) {
	// A structured strategy other than the rule's declared one stays
	// rejected.
	d := mustDescriptor(t, "[[files]]\npath = \"notes.txt\"\npatch = \"text-patch\"\n")
	trg := mustTarget(t, "[overrides.\"notes.txt\"]\nstrategy = \"json-patch\"\npatches = [\"patches/x.json\"]\n")
	_, err := Materialize(tree("notes.txt", "x\n"), d, trg, nil)
	wantErr(t, err, `strategy "json-patch" does not match source-permitted strategy "text-patch"`)
}

func TestPatchPermissionThroughGlob(t *testing.T) {
	// §23/§24: a glob rule grants patch permission to each matched file; the
	// target overrides one concrete file.
	d := mustDescriptor(t, "[[files]]\npath = \"config/**/*.yml\"\npatch = \"json-patch\"\n")
	trg := mustTarget(t, `
[overrides."config/production/service.yml"]
strategy = "json-patch"
patches = ["patches/production-service.json"]
`)
	src := tree(
		"config/production/service.yml", "replicas: 1\n",
		"config/staging/service.yml", "replicas: 1\n",
	)
	patches := map[string][]byte{
		"patches/production-service.json": []byte(`[{"op":"replace","path":"/replicas","value":3}]`),
	}
	got, err := Materialize(src, d, trg, patches)
	if err != nil {
		t.Fatal(err)
	}
	yamlEqual(t, got["config/production/service.yml"], "replicas: 3\n")
	if string(got["config/staging/service.yml"]) != "replicas: 1\n" {
		t.Errorf("staging changed: %q", got["config/staging/service.yml"])
	}
}

func TestJSONPatchAgainstJSON(t *testing.T) {
	d := mustDescriptor(t, "[[files]]\npath = \"app.json\"\npatch = \"json-patch\"\n")
	trg := mustTarget(t, "[overrides.\"app.json\"]\nstrategy = \"json-patch\"\npatches = [\"patches/app.json\"]\n")
	patches := map[string][]byte{
		"patches/app.json": []byte(`[{"op":"add","path":"/b","value":2},{"op":"remove","path":"/a"}]`),
	}
	got, err := Materialize(tree("app.json", `{"a": 1}`), d, trg, patches)
	if err != nil {
		t.Fatal(err)
	}
	if string(got["app.json"]) != "{\n  \"b\": 2\n}\n" {
		t.Errorf("got %q", got["app.json"])
	}
}

func TestJSONPatchAgainstYAML(t *testing.T) {
	d := mustDescriptor(t, patchableDescriptor)
	trg := mustTarget(t, "[overrides.\".golangci.yml\"]\nstrategy = \"json-patch\"\npatches = [\"patches/golangci.json\"]\n")
	src := tree(".golangci.yml", "run:\n  timeout: 5m\nlinters:\n  enable:\n    - govet\n", "plain.txt", "x")
	patches := map[string][]byte{
		"patches/golangci.json": []byte(`[{"op":"replace","path":"/run/timeout","value":"10m"}]`),
	}
	got, err := Materialize(src, d, trg, patches)
	if err != nil {
		t.Fatal(err)
	}
	yamlEqual(t, got[".golangci.yml"], "run:\n  timeout: 10m\nlinters:\n  enable:\n    - govet\n")
}

func TestMultiplePatchesInOrder(t *testing.T) {
	d := mustDescriptor(t, "[[files]]\npath = \"a.json\"\npatch = \"json-patch\"\n")
	trg := mustTarget(t, "[overrides.\"a.json\"]\nstrategy = \"json-patch\"\npatches = [\"patches/1.json\", \"patches/2.json\"]\n")
	patches := map[string][]byte{
		"patches/1.json": []byte(`[{"op":"add","path":"/v","value":1}]`),
		"patches/2.json": []byte(`[{"op":"replace","path":"/v","value":2}]`), // requires patch 1 first
	}
	got, err := Materialize(tree("a.json", `{}`), d, trg, patches)
	if err != nil {
		t.Fatal(err)
	}
	if string(got["a.json"]) != "{\n  \"v\": 2\n}\n" {
		t.Errorf("got %q", got["a.json"])
	}
}

func TestMalformedJSONPatch(t *testing.T) {
	d := mustDescriptor(t, patchableDescriptor)
	trg := mustTarget(t, "[overrides.\".golangci.yml\"]\nstrategy = \"json-patch\"\npatches = [\"patches/bad.json\"]\n")
	patches := map[string][]byte{"patches/bad.json": []byte(`{"not": "an array"`)}
	_, err := Materialize(tree(".golangci.yml", "a: 1\n", "plain.txt", "x"), d, trg, patches)
	wantErr(t, err, "patches/bad.json: malformed JSON Patch")
}

func TestJSONPatchOperationFailure(t *testing.T) {
	d := mustDescriptor(t, patchableDescriptor)
	trg := mustTarget(t, "[overrides.\".golangci.yml\"]\nstrategy = \"json-patch\"\npatches = [\"patches/golangci.json\"]\n")
	patches := map[string][]byte{
		"patches/golangci.json": []byte(
			`[{"op":"add","path":"/a","value":1},{"op":"replace","path":"/run/timeout","value":"10m"}]`),
	}
	_, err := Materialize(tree(".golangci.yml", "b: 1\n", "plain.txt", "x"), d, trg, patches)
	// §37: the error names the file, the patch, and the failed operation.
	wantErr(t, err, ".golangci.yml")
	wantErr(t, err, "patches/golangci.json: operation 1 failed")
}

func TestJSONPatchMalformedStructuralInput(t *testing.T) {
	d := mustDescriptor(t, patchableDescriptor)
	trg := mustTarget(t, "[overrides.\".golangci.yml\"]\nstrategy = \"json-patch\"\npatches = [\"patches/x.json\"]\n")
	patches := map[string][]byte{"patches/x.json": []byte(`[{"op":"add","path":"/a","value":1}]`)}
	src := tree(".golangci.yml", "a: [unclosed\n", "plain.txt", "x")
	_, err := Materialize(src, d, trg, patches)
	wantErr(t, err, "invalid YAML")
}

func TestJSONPatchUnsupportedExtension(t *testing.T) {
	d := mustDescriptor(t, "[[files]]\npath = \"data.txt\"\npatch = \"json-patch\"\n")
	trg := mustTarget(t, "[overrides.\"data.txt\"]\nstrategy = \"json-patch\"\npatches = [\"patches/x.json\"]\n")
	patches := map[string][]byte{"patches/x.json": []byte(`[]`)}
	_, err := Materialize(tree("data.txt", "{}"), d, trg, patches)
	wantErr(t, err, "requires a .json, .yml or .yaml file extension")
}

func TestMissingPatchFile(t *testing.T) {
	d := mustDescriptor(t, patchableDescriptor)
	trg := mustTarget(t, "[overrides.\".golangci.yml\"]\nstrategy = \"json-patch\"\npatches = [\"patches/gone.json\"]\n")
	_, err := Materialize(tree(".golangci.yml", "a: 1\n", "plain.txt", "x"), d, trg, nil)
	wantErr(t, err, "patch file patches/gone.json not found")
}

func TestTextPatchSuccessAndSubstitutionBeforePatch(t *testing.T) {
	// The patch context expects the substituted value, proving substitution
	// precedes patching (§25).
	d := mustDescriptor(t, `
[[arguments]]
name = "project_name"
token = "@@PROJECT_NAME@@"

[[files]]
path = "notes.txt"
patch = "text-patch"
`)
	trg := mustTarget(t, `
[args]
project_name = "orders"

[overrides."notes.txt"]
strategy = "text-patch"
patches = ["patches/notes.diff"]
`)
	patches := map[string][]byte{"patches/notes.diff": []byte(`--- a/notes.txt
+++ b/notes.txt
@@ -1,3 +1,3 @@
 hello orders
-line two
+line 2
 line three
`)}
	got, err := Materialize(tree("notes.txt", "hello @@PROJECT_NAME@@\nline two\nline three\n"), d, trg, patches)
	if err != nil {
		t.Fatal(err)
	}
	if string(got["notes.txt"]) != "hello orders\nline 2\nline three\n" {
		t.Errorf("got %q", got["notes.txt"])
	}
}

func TestTextPatchFailure(t *testing.T) {
	d := mustDescriptor(t, "[[files]]\npath = \"notes.txt\"\npatch = \"text-patch\"\n")
	trg := mustTarget(t, "[overrides.\"notes.txt\"]\nstrategy = \"text-patch\"\npatches = [\"patches/notes.diff\"]\n")
	patches := map[string][]byte{"patches/notes.diff": []byte(`--- a/notes.txt
+++ b/notes.txt
@@ -1,2 +1,2 @@
 different context
-line two
+line 2
`)}
	_, err := Materialize(tree("notes.txt", "hello\nline two\n"), d, trg, patches)
	wantErr(t, err, "patches/notes.diff: hunk 1 failed")
}

func TestTextPatchMalformed(t *testing.T) {
	d := mustDescriptor(t, "[[files]]\npath = \"notes.txt\"\npatch = \"text-patch\"\n")
	trg := mustTarget(t, "[overrides.\"notes.txt\"]\nstrategy = \"text-patch\"\npatches = [\"patches/notes.diff\"]\n")
	patches := map[string][]byte{"patches/notes.diff": []byte("this is not a diff\n")}
	_, err := Materialize(tree("notes.txt", "hello\n"), d, trg, patches)
	wantErr(t, err, "malformed unified diff")
}

func TestTextPatchNoFinalNewline(t *testing.T) {
	d := mustDescriptor(t, "[[files]]\npath = \"f.txt\"\npatch = \"text-patch\"\n")
	trg := mustTarget(t, "[overrides.\"f.txt\"]\nstrategy = \"text-patch\"\npatches = [\"patches/p.diff\"]\n")
	patches := map[string][]byte{"patches/p.diff": []byte(`--- a/f.txt
+++ b/f.txt
@@ -1 +1 @@
-old
+new
\ No newline at end of file
`)}
	got, err := Materialize(tree("f.txt", "old\n"), d, trg, patches)
	if err != nil {
		t.Fatal(err)
	}
	if string(got["f.txt"]) != "new" {
		t.Errorf("got %q, want %q", got["f.txt"], "new")
	}
}

// --- §54 acceptance scenario (pure materialization part) ---

const acceptanceDescriptor = `
[scaffold]
version = 1

[template]
name = "go-service"

[[arguments]]
name = "project_name"
description = "Project name"
token = "@@PROJECT_NAME@@"

[[arguments]]
name = "module"

[[arguments]]
name = "go_version"
default = "1.26"
token = "@@GO_VERSION@@"

[[files]]
path = "Makefile"

[[files]]
path = "go.mod"

[files.arguments.module]
token = "@@MODULE@@"

[[files]]
path = ".golangci.yml"
patch = "json-patch"

[[files]]
path = ".github/workflows/*.yml"
`

const acceptanceTarget = `
[scaffold]
version = 1

[source]
git = "https://github.com/acme/go-template.git"
ref = "main"

[args]
project_name = "orders"
module = "github.com/acme/orders"

[overrides.".golangci.yml"]
strategy = "json-patch"
patches = [
    "patches/golangci.json"
]
`

func acceptanceInputs(t *testing.T) (*config.Descriptor, *config.TargetConfig, map[string][]byte) {
	t.Helper()
	d, err := config.ParseDescriptor([]byte(acceptanceDescriptor))
	if err != nil {
		t.Fatal(err)
	}
	trg, err := config.ParseTarget([]byte(acceptanceTarget))
	if err != nil {
		t.Fatal(err)
	}
	patches := map[string][]byte{
		"patches/golangci.json": []byte(`[{"op":"replace","path":"/run/timeout","value":"10m"}]`),
	}
	return d, trg, patches
}

func TestAcceptanceScenario(t *testing.T) {
	d, trg, patches := acceptanceInputs(t)

	commitA := tree(
		"Makefile", "PROJECT=@@PROJECT_NAME@@\nGO=@@GO_VERSION@@\n",
		"go.mod", "module @@MODULE@@\n\ngo @@GO_VERSION@@\n",
		".golangci.yml", "run:\n  timeout: 5m\n",
		".github/workflows/ci.yml", "name: ci-@@PROJECT_NAME@@\n",
		".github/workflows/test.yml", "name: test\n",
		"README.md", "unmanaged\n",
	)
	got, err := Materialize(commitA, d, trg, patches)
	if err != nil {
		t.Fatal(err)
	}
	if string(got["Makefile"]) != "PROJECT=orders\nGO=1.26\n" {
		t.Errorf("Makefile: %q", got["Makefile"])
	}
	if string(got["go.mod"]) != "module github.com/acme/orders\n\ngo 1.26\n" {
		t.Errorf("go.mod: %q", got["go.mod"])
	}
	yamlEqual(t, got[".golangci.yml"], "run:\n  timeout: 10m\n")
	if string(got[".github/workflows/ci.yml"]) != "name: ci-orders\n" {
		t.Errorf("ci.yml: %q", got[".github/workflows/ci.yml"])
	}
	if _, ok := got["README.md"]; ok {
		t.Error("unmanaged README.md materialized")
	}
	if len(got) != 5 {
		t.Errorf("managed set = %d files", len(got))
	}

	// Commit B: Makefile and go.mod change, .golangci.yml changes compatibly,
	// test.yml is removed, security.yml is added and matches the glob.
	commitB := tree(
		"Makefile", "PROJECT=@@PROJECT_NAME@@\nGO=@@GO_VERSION@@\nlint:\n",
		"go.mod", "module @@MODULE@@\n\ngo @@GO_VERSION@@\n\nrequire example.com/dep v1.0.0\n",
		".golangci.yml", "run:\n  timeout: 5m\n  tests: true\n",
		".github/workflows/ci.yml", "name: ci-@@PROJECT_NAME@@\n",
		".github/workflows/security.yml", "name: security\n",
		"README.md", "unmanaged\n",
	)
	gotB, err := Materialize(commitB, d, trg, patches)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := gotB[".github/workflows/test.yml"]; ok {
		t.Error("removed test.yml still materialized")
	}
	if string(gotB[".github/workflows/security.yml"]) != "name: security\n" {
		t.Errorf("security.yml: %q", gotB[".github/workflows/security.yml"])
	}
	if string(gotB["go.mod"]) != "module github.com/acme/orders\n\ngo 1.26\n\nrequire example.com/dep v1.0.0\n" {
		t.Errorf("go.mod: %q", gotB["go.mod"])
	}
	yamlEqual(t, gotB[".golangci.yml"], "run:\n  timeout: 10m\n  tests: true\n")
}

func TestAcceptanceIncompatiblePatchFails(t *testing.T) {
	// Commit B drops /run, so the target patch no longer applies: the whole
	// materialization must fail (§54).
	d, trg, patches := acceptanceInputs(t)
	commitB := tree(
		"Makefile", "PROJECT=@@PROJECT_NAME@@\n",
		"go.mod", "module @@MODULE@@\n",
		".golangci.yml", "linters:\n  enable:\n    - govet\n",
		".github/workflows/ci.yml", "name: ci\n",
	)
	_, err := Materialize(commitB, d, trg, patches)
	wantErr(t, err, "patches/golangci.json: operation 0 failed")
}
