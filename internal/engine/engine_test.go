package engine

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// setEnv pins git behavior so tests pass on any machine: no user/system
// config, fixed identity, and an isolated scaffold cache.
func setEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "no-global-config"))
	t.Setenv("GIT_AUTHOR_NAME", "test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
	t.Setenv("GIT_SCAFFOLD_CACHE", filepath.Join(t.TempDir(), "cache"))
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	return dir
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		abs := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func commitAll(t *testing.T, dir, msg string) string {
	t.Helper()
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", msg)
	return runGit(t, dir, "rev-parse", "HEAD")
}

func readFile(t *testing.T, dir, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

// snapshot captures every file (path → content) under root except .git,
// for verifying that failed or read-only operations change nothing.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		out[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameSnapshot(t *testing.T, a, b map[string]string) {
	t.Helper()
	if len(a) != len(b) {
		t.Fatalf("file sets differ: %d vs %d files\n%v\n%v", len(a), len(b), sortedKeys(a), sortedKeys(b))
	}
	for p, c := range a {
		if b[p] != c {
			t.Fatalf("file %s changed", p)
		}
	}
}

const descriptorA = `
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

// sourceRepoA builds the §54 commit-A source repository.
func sourceRepoA(t *testing.T) (dir, sha string) {
	t.Helper()
	dir = newRepo(t)
	writeFiles(t, dir, map[string]string{
		".git-scaffold/config.toml":  descriptorA,
		"Makefile":                   "build:\n\tgo build ./...\n",
		"go.mod":                     "module @@MODULE@@\n\ngo @@GO_VERSION@@\n",
		".golangci.yml":              "run:\n  timeout: 5m\n",
		".github/workflows/ci.yml":   "name: ci\nproject: @@PROJECT_NAME@@\n",
		".github/workflows/test.yml": "name: test\nproject: @@PROJECT_NAME@@\n",
		"README.md":                  "unmanaged\n",
	})
	return dir, commitAll(t, dir, "A")
}

// advanceToB applies the §54 commit-B changes to the source repository.
func advanceToB(t *testing.T, dir string) string {
	t.Helper()
	writeFiles(t, dir, map[string]string{
		"Makefile":                       "build:\n\tgo build ./...\n\ntest:\n\tgo test ./...\n",
		"go.mod":                         "module @@MODULE@@\n\ngo @@GO_VERSION@@\n\n// scaffolded\n",
		".golangci.yml":                  "run:\n  timeout: 6m\n  tests: true\n",
		".github/workflows/security.yml": "name: security\nproject: @@PROJECT_NAME@@\n",
	})
	if err := os.Remove(filepath.Join(dir, ".github", "workflows", "test.yml")); err != nil {
		t.Fatal(err)
	}
	return commitAll(t, dir, "B")
}

const golangciPatch = `[{"op":"replace","path":"/run/timeout","value":"10m"}]`

// writeTargetConfig writes a §54-style target configuration by hand
// (including the json-patch override, which init does not generate).
func writeTargetConfig(t *testing.T, dir, srcURL string) {
	t.Helper()
	writeFiles(t, dir, map[string]string{
		".git-scaffold/config.toml": `
[scaffold]
version = 1

[source]
git = "` + filepath.ToSlash(srcURL) + `"
ref = "main"

[args]
project_name = "orders"
module = "github.com/acme/orders"

[overrides.".golangci.yml"]
strategy = "json-patch"
patches = ["patches/golangci.json"]
`,
		".git-scaffold/patches/golangci.json": golangciPatch,
	})
}

// appliedTarget builds source@A plus a target repository with the locked
// materialization applied.
func appliedTarget(t *testing.T) (srcDir, shaA, tgtDir string) {
	t.Helper()
	srcDir, shaA = sourceRepoA(t)
	tgtDir = newRepo(t)
	writeTargetConfig(t, tgtDir, srcDir)
	var out bytes.Buffer
	if err := Apply(tgtDir, &out, false); err != nil {
		t.Fatalf("initial apply: %v", err)
	}
	return srcDir, shaA, tgtDir
}

func mustCheckClean(t *testing.T, dir string) {
	t.Helper()
	var out bytes.Buffer
	clean, err := Check(dir, &out)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !clean {
		t.Fatalf("check not clean:\n%s", out.String())
	}
}

func TestApplyInitialAndCheck(t *testing.T) {
	setEnv(t)
	_, shaA, tgt := appliedTarget(t)

	if got := readFile(t, tgt, ".git-scaffold/lock"); got != shaA+"\n" {
		t.Fatalf("lock = %q, want %q", got, shaA+"\n")
	}
	if got := readFile(t, tgt, "go.mod"); got != "module github.com/acme/orders\n\ngo 1.26\n" {
		t.Fatalf("go.mod = %q", got)
	}
	if got := readFile(t, tgt, ".github/workflows/ci.yml"); !strings.Contains(got, "project: orders") {
		t.Fatalf("ci.yml = %q", got)
	}
	if got := readFile(t, tgt, ".golangci.yml"); !strings.Contains(got, "10m") {
		t.Fatalf("patch not applied: %q", got)
	}
	if _, err := os.Stat(filepath.Join(tgt, "README.md")); !os.IsNotExist(err) {
		t.Fatalf("unmanaged source file materialized")
	}
	mustCheckClean(t, tgt)
}

func TestOutsideGitRepo(t *testing.T) {
	setEnv(t)
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", dir)
	_, err := Check(filepath.Join(dir), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "worktree") {
		t.Fatalf("err = %v", err)
	}
}

func TestMissingConfig(t *testing.T) {
	setEnv(t)
	dir := newRepo(t)
	_, err := Check(dir, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "no scaffold configuration") {
		t.Fatalf("err = %v", err)
	}
}

func TestInvalidTOML(t *testing.T) {
	setEnv(t)
	dir := newRepo(t)
	writeFiles(t, dir, map[string]string{".git-scaffold/config.toml": "not = [ toml"})
	_, err := Check(dir, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "invalid TOML") {
		t.Fatalf("err = %v", err)
	}
}

func TestUnsupportedVersion(t *testing.T) {
	setEnv(t)
	dir := newRepo(t)
	writeFiles(t, dir, map[string]string{
		".git-scaffold/config.toml": "[scaffold]\nversion = 2\n\n[source]\ngit = \"x\"\n",
	})
	_, err := Check(dir, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "unsupported scaffold version") {
		t.Fatalf("err = %v", err)
	}
}

func TestInvalidSource(t *testing.T) {
	setEnv(t)
	dir := newRepo(t)
	missing := filepath.ToSlash(filepath.Join(t.TempDir(), "no-such-repo"))
	writeFiles(t, dir, map[string]string{
		".git-scaffold/config.toml": "[scaffold]\nversion = 1\n\n[source]\ngit = \"" + missing + "\"\n",
	})
	err := Apply(dir, io.Discard, false)
	if err == nil || !strings.Contains(err.Error(), "cannot resolve source") {
		t.Fatalf("err = %v", err)
	}
}

func TestUnresolvableRef(t *testing.T) {
	setEnv(t)
	src, _ := sourceRepoA(t)
	dir := newRepo(t)
	writeFiles(t, dir, map[string]string{
		".git-scaffold/config.toml": "[scaffold]\nversion = 1\n\n[source]\ngit = \"" +
			filepath.ToSlash(src) + "\"\nref = \"does-not-exist\"\n\n[args]\nproject_name = \"x\"\nmodule = \"y\"\n",
	})
	err := Apply(dir, io.Discard, false)
	if err == nil || !strings.Contains(err.Error(), "cannot resolve ref") {
		t.Fatalf("err = %v", err)
	}
}

func TestMissingDescriptor(t *testing.T) {
	setEnv(t)
	src := newRepo(t)
	writeFiles(t, src, map[string]string{"README.md": "hi\n"})
	commitAll(t, src, "no descriptor")
	dir := newRepo(t)
	writeFiles(t, dir, map[string]string{
		".git-scaffold/config.toml": "[scaffold]\nversion = 1\n\n[source]\ngit = \"" +
			filepath.ToSlash(src) + "\"\n",
	})
	err := Apply(dir, io.Discard, false)
	if err == nil || !strings.Contains(err.Error(), "no scaffold descriptor") {
		t.Fatalf("err = %v", err)
	}
}

func TestMalformedDescriptor(t *testing.T) {
	setEnv(t)
	src := newRepo(t)
	writeFiles(t, src, map[string]string{".git-scaffold/config.toml": "version = [ nope"})
	commitAll(t, src, "bad descriptor")
	dir := newRepo(t)
	writeFiles(t, dir, map[string]string{
		".git-scaffold/config.toml": "[scaffold]\nversion = 1\n\n[source]\ngit = \"" +
			filepath.ToSlash(src) + "\"\n",
	})
	err := Apply(dir, io.Discard, false)
	if err == nil || !strings.Contains(err.Error(), "source descriptor") {
		t.Fatalf("err = %v", err)
	}
}

func TestMalformedLock(t *testing.T) {
	setEnv(t)
	_, _, tgt := appliedTarget(t)
	writeFiles(t, tgt, map[string]string{".git-scaffold/lock": "garbage\n"})
	_, err := Check(tgt, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "malformed lock") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckDetectsManualEditAndMissing(t *testing.T) {
	setEnv(t)
	_, _, tgt := appliedTarget(t)
	writeFiles(t, tgt, map[string]string{"Makefile": "tampered\n"})
	if err := os.Remove(filepath.Join(tgt, "go.mod")); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	clean, err := Check(tgt, &out)
	if err != nil {
		t.Fatal(err)
	}
	if clean {
		t.Fatal("check reported clean")
	}
	if !strings.Contains(out.String(), "modified: Makefile") ||
		!strings.Contains(out.String(), "missing:  go.mod") {
		t.Fatalf("output:\n%s", out.String())
	}
}

func TestDiffOutputAndNoModification(t *testing.T) {
	setEnv(t)
	_, _, tgt := appliedTarget(t)
	writeFiles(t, tgt, map[string]string{"Makefile": "build:\n\tgo build ./...\nextra line\n"})
	if err := os.Remove(filepath.Join(tgt, "go.mod")); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, tgt)
	var out bytes.Buffer
	if err := Diff(tgt, &out); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{
		"--- a/Makefile", "+++ b/Makefile", "+extra line\n", "@@ ",
		"--- a/go.mod", "+++ /dev/null", "-module github.com/acme/orders",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("diff missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, ".golangci.yml") {
		t.Fatalf("diff reported unchanged file:\n%s", s)
	}
	sameSnapshot(t, before, snapshot(t, tgt))
}

func TestApplyDoesNotAdvanceLock(t *testing.T) {
	setEnv(t)
	src, shaA, tgt := appliedTarget(t)
	advanceToB(t, src)
	var out bytes.Buffer
	if err := Apply(tgt, &out, false); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, tgt, ".git-scaffold/lock"); got != shaA+"\n" {
		t.Fatalf("apply advanced the lock: %q", got)
	}
	if got := readFile(t, tgt, "Makefile"); strings.Contains(got, "go test") {
		t.Fatalf("apply materialized commit B content: %q", got)
	}
}

func TestApplyRefusesDriftWithoutForce(t *testing.T) {
	setEnv(t)
	_, _, tgt := appliedTarget(t)
	writeFiles(t, tgt, map[string]string{"Makefile": "hand edited\n"})
	err := Apply(tgt, io.Discard, false)
	if err == nil || !strings.Contains(err.Error(), "local modifications") {
		t.Fatalf("err = %v", err)
	}
	if got := readFile(t, tgt, "Makefile"); got != "hand edited\n" {
		t.Fatalf("refused apply still wrote: %q", got)
	}
	if err := Apply(tgt, io.Discard, true); err != nil {
		t.Fatal(err)
	}
	mustCheckClean(t, tgt)
}

func TestCheckOfflineFromCache(t *testing.T) {
	setEnv(t)
	src, _, tgt := appliedTarget(t)
	// The locked commit is cached; check must work with the source gone
	// (§50: no network needed when the locked commit is available).
	if err := os.Rename(src, src+".moved"); err != nil {
		t.Fatal(err)
	}
	mustCheckClean(t, tgt)
}

func TestUpdateUnchangedRef(t *testing.T) {
	setEnv(t)
	_, shaA, tgt := appliedTarget(t)
	before := snapshot(t, tgt)
	var out bytes.Buffer
	if err := Update(tgt, &out, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "already up to date") {
		t.Fatalf("output:\n%s", out.String())
	}
	sameSnapshot(t, before, snapshot(t, tgt))
	if got := readFile(t, tgt, ".git-scaffold/lock"); got != shaA+"\n" {
		t.Fatalf("lock changed: %q", got)
	}
}

// TestUpdateAcceptance is the §54 acceptance scenario: commit A → commit B
// with modifications, a glob-matched addition, and a removal.
func TestUpdateAcceptance(t *testing.T) {
	setEnv(t)
	src, shaA, tgt := appliedTarget(t)
	shaB := advanceToB(t, src)
	var out bytes.Buffer
	if err := Update(tgt, &out, false); err != nil {
		t.Fatalf("update: %v\n%s", err, out.String())
	}
	s := out.String()
	for _, want := range []string{
		short(shaA) + " → " + short(shaB),
		"M Makefile\n", "M go.mod\n", "M .golangci.yml\n",
		"A .github/workflows/security.yml\n",
		"D .github/workflows/test.yml\n",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("output missing %q:\n%s", want, s)
		}
	}
	if got := readFile(t, tgt, ".git-scaffold/lock"); got != shaB+"\n" {
		t.Fatalf("lock = %q, want commit B", got)
	}
	if _, err := os.Stat(filepath.Join(tgt, ".github", "workflows", "test.yml")); !os.IsNotExist(err) {
		t.Fatal("removed managed file still present")
	}
	if got := readFile(t, tgt, ".github/workflows/security.yml"); !strings.Contains(got, "project: orders") {
		t.Fatalf("security.yml = %q", got)
	}
	if got := readFile(t, tgt, ".golangci.yml"); !strings.Contains(got, "10m") || !strings.Contains(got, "tests") {
		t.Fatalf(".golangci.yml = %q", got)
	}
	if got := readFile(t, tgt, "Makefile"); !strings.Contains(got, "go test") {
		t.Fatalf("Makefile = %q", got)
	}
	mustCheckClean(t, tgt)
}

func TestUpdateFailurePreservesState(t *testing.T) {
	setEnv(t)
	src, shaA, tgt := appliedTarget(t)
	// Commit B drops run.timeout, making the target's json patch
	// inapplicable (§54: the update must fail and change nothing).
	writeFiles(t, src, map[string]string{".golangci.yml": "run:\n  retries: 2\n"})
	commitAll(t, src, "incompatible")
	before := snapshot(t, tgt)
	var out bytes.Buffer
	err := Update(tgt, &out, false)
	if err != ErrUpdateFailed {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out.String(), "No files changed.") {
		t.Fatalf("output:\n%s", out.String())
	}
	sameSnapshot(t, before, snapshot(t, tgt))
	if got := readFile(t, tgt, ".git-scaffold/lock"); got != shaA+"\n" {
		t.Fatalf("failed update advanced lock: %q", got)
	}
}

func TestUpdateRefusesUnrelatedDrift(t *testing.T) {
	setEnv(t)
	src, _, tgt := appliedTarget(t)
	writeFiles(t, src, map[string]string{"Makefile": "build:\n\tgo build -v ./...\n"})
	commitAll(t, src, "B")
	writeFiles(t, tgt, map[string]string{"Makefile": "hand edited\n"})
	before := snapshot(t, tgt)
	var out bytes.Buffer
	err := Update(tgt, &out, false)
	if err != ErrUpdateFailed || !strings.Contains(out.String(), "local modifications") ||
		!strings.Contains(out.String(), "Makefile") {
		t.Fatalf("err = %v, output:\n%s", err, out.String())
	}
	sameSnapshot(t, before, snapshot(t, tgt))

	if err := Update(tgt, io.Discard, true); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, tgt, "Makefile"); !strings.Contains(got, "go build -v") {
		t.Fatalf("Makefile = %q", got)
	}
	mustCheckClean(t, tgt)
}

func TestOutdated(t *testing.T) {
	setEnv(t)
	src, shaA, tgt := appliedTarget(t)
	var out bytes.Buffer
	outdated, err := Outdated(tgt, &out)
	if err != nil || outdated {
		t.Fatalf("outdated = %v, err = %v", outdated, err)
	}
	shaB := advanceToB(t, src)
	before := snapshot(t, tgt)
	out.Reset()
	outdated, err = Outdated(tgt, &out)
	if err != nil || !outdated {
		t.Fatalf("outdated = %v, err = %v", outdated, err)
	}
	if !strings.Contains(out.String(), shaA) || !strings.Contains(out.String(), shaB) {
		t.Fatalf("output missing SHAs:\n%s", out.String())
	}
	sameSnapshot(t, before, snapshot(t, tgt))
}

func TestInit(t *testing.T) {
	setEnv(t)
	src, shaA := sourceRepoA(t)
	tgt := newRepo(t)
	args := map[string]string{"project_name": "orders", "module": "github.com/acme/orders"}
	var out bytes.Buffer
	if err := Init(tgt, &out, filepath.ToSlash(src), "main", args, false, false, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "A Makefile\n") {
		t.Fatalf("output:\n%s", out.String())
	}
	if got := readFile(t, tgt, ".git-scaffold/lock"); got != shaA+"\n" {
		t.Fatalf("lock = %q", got)
	}
	cfg := readFile(t, tgt, ".git-scaffold/config.toml")
	if !strings.Contains(cfg, "version = 1") || !strings.Contains(cfg, `ref = "main"`) ||
		!strings.Contains(cfg, `project_name = "orders"`) {
		t.Fatalf("config.toml:\n%s", cfg)
	}
	mustCheckClean(t, tgt)

	// A second init must refuse.
	err := Init(tgt, io.Discard, filepath.ToSlash(src), "main", args, false, false, false)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v", err)
	}
}

func TestInitMissingRequiredArgs(t *testing.T) {
	setEnv(t)
	src, _ := sourceRepoA(t)
	tgt := newRepo(t)
	err := Init(tgt, io.Discard, filepath.ToSlash(src), "main",
		map[string]string{"project_name": "orders"}, false, false, false)
	if err == nil || !strings.Contains(err.Error(), "missing required arguments: module") {
		t.Fatalf("err = %v", err)
	}
	// Nothing may be written on failure.
	if got := snapshot(t, tgt); len(got) != 0 {
		t.Fatalf("init wrote files: %v", sortedKeys(got))
	}
}

func TestInitDefaultRefAndSubdirectory(t *testing.T) {
	setEnv(t)
	src, shaA := sourceRepoA(t)
	tgt := newRepo(t)
	sub := filepath.Join(tgt, "sub", "deeper")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	args := map[string]string{"project_name": "orders", "module": "github.com/acme/orders"}
	// Empty ref uses the remote HEAD (§6); commands run from subdirectories
	// operate on the worktree root (§49).
	if err := Init(sub, io.Discard, filepath.ToSlash(src), "", args, false, false, false); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, tgt, ".git-scaffold/lock"); got != shaA+"\n" {
		t.Fatalf("lock = %q", got)
	}
	mustCheckClean(t, sub)
}

var initArgs = map[string]string{"project_name": "orders", "module": "github.com/acme/orders"}

func TestInitExistingAdoptsDifferences(t *testing.T) {
	setEnv(t)
	src, shaA := sourceRepoA(t)
	tgt := newRepo(t)
	// Makefile differs from the materialization and has no trailing newline;
	// go.mod already matches the materialization exactly; the workflow files
	// do not exist yet.
	localMakefile := "build:\n\tgo build ./...\n\nlint:\n\tgo vet ./..."
	writeFiles(t, tgt, map[string]string{
		"Makefile": localMakefile,
		"go.mod":   "module github.com/acme/orders\n\ngo 1.26\n",
	})
	var out bytes.Buffer
	if err := Init(tgt, &out, filepath.ToSlash(src), "main", initArgs, true, false, false); err != nil {
		t.Fatal(err)
	}
	// The differing pre-existing file is untouched byte-for-byte.
	if got := readFile(t, tgt, "Makefile"); got != localMakefile {
		t.Fatalf("Makefile modified: %q", got)
	}
	// Its difference is captured as a registered text patch.
	patch := readFile(t, tgt, ".git-scaffold/patches/Makefile.patch")
	if !strings.Contains(patch, "+lint:") || !strings.Contains(patch, "No newline at end of file") {
		t.Fatalf("patch:\n%s", patch)
	}
	cfg := readFile(t, tgt, ".git-scaffold/config.toml")
	if !strings.Contains(cfg, `strategy = "text-patch"`) ||
		!strings.Contains(cfg, `"patches/Makefile.patch"`) {
		t.Fatalf("config.toml:\n%s", cfg)
	}
	// The identical file needs no patch.
	if _, err := os.Stat(filepath.Join(tgt, ".git-scaffold", "patches", "go.mod.patch")); !os.IsNotExist(err) {
		t.Fatal("identical file got a patch")
	}
	if strings.Contains(cfg, "go.mod.patch") {
		t.Fatalf("config.toml has go.mod override:\n%s", cfg)
	}
	// Missing managed files are still materialized.
	if got := readFile(t, tgt, ".github/workflows/ci.yml"); got != "name: ci\nproject: orders\n" {
		t.Fatalf("ci.yml = %q", got)
	}
	if got := readFile(t, tgt, ".git-scaffold/lock"); got != shaA+"\n" {
		t.Fatalf("lock = %q", got)
	}
	if !strings.Contains(out.String(), "P Makefile (text-patch)\n") ||
		!strings.Contains(out.String(), "adopted 1 existing file as patches") {
		t.Fatalf("output:\n%s", out.String())
	}
	// The §33 guarantee: check passes immediately after init --existing.
	mustCheckClean(t, tgt)
}

func TestInitExistingWithoutDifferences(t *testing.T) {
	setEnv(t)
	src, shaA := sourceRepoA(t)
	tgt := newRepo(t)
	var out bytes.Buffer
	if err := Init(tgt, &out, filepath.ToSlash(src), "main", initArgs, true, false, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "adopted") {
		t.Fatalf("output:\n%s", out.String())
	}
	if cfg := readFile(t, tgt, ".git-scaffold/config.toml"); strings.Contains(cfg, "overrides") {
		t.Fatalf("config.toml:\n%s", cfg)
	}
	if got := readFile(t, tgt, ".git-scaffold/lock"); got != shaA+"\n" {
		t.Fatalf("lock = %q", got)
	}
	mustCheckClean(t, tgt)
}

func TestInitExistingBinaryRefusalAndForce(t *testing.T) {
	setEnv(t)
	src, _ := sourceRepoA(t)
	tgt := newRepo(t)
	writeFiles(t, tgt, map[string]string{
		".github/workflows/ci.yml": "name: ci\x00binary\n",
		"Makefile":                 "build:\n\tgo build ./...\n# local\n",
	})
	before := snapshot(t, tgt)
	err := Init(tgt, io.Discard, filepath.ToSlash(src), "main", initArgs, true, false, false)
	if err == nil || !strings.Contains(err.Error(), "binary files differ") ||
		!strings.Contains(err.Error(), ".github/workflows/ci.yml") {
		t.Fatalf("err = %v", err)
	}
	sameSnapshot(t, before, snapshot(t, tgt))

	// --force resolves only the binary refusal: the binary file is
	// overwritten while the text-adoptable file is still adopted.
	var out bytes.Buffer
	if err := Init(tgt, &out, filepath.ToSlash(src), "main", initArgs, true, true, false); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, tgt, ".github/workflows/ci.yml"); got != "name: ci\nproject: orders\n" {
		t.Fatalf("ci.yml = %q", got)
	}
	if got := readFile(t, tgt, "Makefile"); got != "build:\n\tgo build ./...\n# local\n" {
		t.Fatalf("Makefile modified: %q", got)
	}
	if !strings.Contains(out.String(), "M .github/workflows/ci.yml\n") ||
		!strings.Contains(out.String(), "P Makefile (text-patch)\n") {
		t.Fatalf("output:\n%s", out.String())
	}
	mustCheckClean(t, tgt)
}

func TestInitRefusesDifferingFilesWithoutExisting(t *testing.T) {
	setEnv(t)
	src, _ := sourceRepoA(t)
	tgt := newRepo(t)
	writeFiles(t, tgt, map[string]string{"Makefile": "hand written\n"})
	before := snapshot(t, tgt)
	err := Init(tgt, io.Discard, filepath.ToSlash(src), "main", initArgs, false, false, false)
	if err == nil || !strings.Contains(err.Error(), "local modifications") ||
		!strings.Contains(err.Error(), "Makefile") {
		t.Fatalf("err = %v", err)
	}
	sameSnapshot(t, before, snapshot(t, tgt))
}

func TestUpdateThroughAdoptedPatch(t *testing.T) {
	setEnv(t)
	src, _ := sourceRepoA(t)
	tgt := newRepo(t)
	writeFiles(t, tgt, map[string]string{
		"Makefile": "build:\n\tgo build ./...\n\nlint:\n\tgo vet ./...\n",
	})
	if err := Init(tgt, io.Discard, filepath.ToSlash(src), "main", initArgs, true, false, false); err != nil {
		t.Fatal(err)
	}
	mustCheckClean(t, tgt)

	// Commit B appends to the Makefile after the adopted patch's context
	// lines, so the patch still applies.
	advanceToB(t, src)
	if err := Update(tgt, io.Discard, false); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, tgt, "Makefile")
	if !strings.Contains(got, "lint:") || !strings.Contains(got, "test:") {
		t.Fatalf("Makefile = %q", got)
	}
	mustCheckClean(t, tgt)

	// Commit C rewrites the lines the adopted patch depends on; the update
	// must fail transactionally.
	writeFiles(t, src, map[string]string{"Makefile": "compile:\n\tgo build -v ./...\n"})
	commitAll(t, src, "C")
	before := snapshot(t, tgt)
	var out bytes.Buffer
	err := Update(tgt, &out, false)
	if err != ErrUpdateFailed || !strings.Contains(out.String(), "No files changed.") {
		t.Fatalf("err = %v, output:\n%s", err, out.String())
	}
	sameSnapshot(t, before, snapshot(t, tgt))
}

func TestSymlinkSafety(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks not reliably available on Windows CI")
	}
	setEnv(t)
	src := newRepo(t)
	writeFiles(t, src, map[string]string{
		".git-scaffold/config.toml": "[scaffold]\nversion = 1\n\n[[files]]\npath = \"sub/inner.txt\"\n",
		"sub/inner.txt":             "content\n",
	})
	commitAll(t, src, "A")

	t.Run("symlinked parent escapes worktree", func(t *testing.T) {
		tgt := newRepo(t)
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(tgt, "sub")); err != nil {
			t.Fatal(err)
		}
		writeFiles(t, tgt, map[string]string{
			".git-scaffold/config.toml": "[scaffold]\nversion = 1\n\n[source]\ngit = \"" +
				filepath.ToSlash(src) + "\"\n",
		})
		err := Apply(tgt, io.Discard, false)
		if err == nil || !strings.Contains(err.Error(), "escapes the worktree") {
			t.Fatalf("err = %v", err)
		}
		if _, err := os.Stat(filepath.Join(outside, "inner.txt")); !os.IsNotExist(err) {
			t.Fatal("wrote through symlinked directory")
		}
	})

	t.Run("symlinked managed path", func(t *testing.T) {
		tgt := newRepo(t)
		outside := filepath.Join(t.TempDir(), "victim")
		if err := os.WriteFile(outside, []byte("original\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(tgt, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(tgt, "sub", "inner.txt")); err != nil {
			t.Fatal(err)
		}
		writeFiles(t, tgt, map[string]string{
			".git-scaffold/config.toml": "[scaffold]\nversion = 1\n\n[source]\ngit = \"" +
				filepath.ToSlash(src) + "\"\n",
		})
		err := Apply(tgt, io.Discard, true)
		if err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("err = %v", err)
		}
		if got, _ := os.ReadFile(outside); string(got) != "original\n" {
			t.Fatalf("wrote through symlink: %q", got)
		}
	})
}

// TestReadPathSymlinkSafety: reads go through the same §48 refusal as
// writes. A managed path symlinked to an outside file must make check,
// diff, and init --existing refuse — never follow the link.
func TestReadPathSymlinkSafety(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks not reliably available on Windows CI")
	}
	setEnv(t)
	src, _, tgt := appliedTarget(t)

	// The outside file holds the exact expected content, so following the
	// symlink would (wrongly) report a clean check.
	outside := filepath.Join(t.TempDir(), "leak")
	if err := os.WriteFile(outside, []byte(readFile(t, tgt, "Makefile")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(tgt, "Makefile")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(tgt, "Makefile")); err != nil {
		t.Fatal(err)
	}

	if _, err := Check(tgt, io.Discard); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("check err = %v", err)
	}
	if err := Diff(tgt, io.Discard); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("diff err = %v", err)
	}

	// init --existing must refuse the symlinked managed path and write
	// nothing.
	tgt2 := newRepo(t)
	if err := os.Symlink(outside, filepath.Join(tgt2, "Makefile")); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, tgt2)
	err := Init(tgt2, io.Discard, filepath.ToSlash(src), "main", initArgs, true, false, false)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("init --existing err = %v", err)
	}
	sameSnapshot(t, before, snapshot(t, tgt2))
}

// TestInitDoesNotReportIdenticalFilesAsAdded: plain init must not print
// "A" for pre-existing files that already match the materialization.
func TestInitDoesNotReportIdenticalFilesAsAdded(t *testing.T) {
	setEnv(t)
	src, _ := sourceRepoA(t)
	tgt := newRepo(t)
	writeFiles(t, tgt, map[string]string{
		"go.mod": "module github.com/acme/orders\n\ngo 1.26\n",
	})
	var out bytes.Buffer
	if err := Init(tgt, &out, filepath.ToSlash(src), "main", initArgs, false, false, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "A go.mod\n") {
		t.Fatalf("pre-existing identical file reported as added:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "A Makefile\n") {
		t.Fatalf("output:\n%s", out.String())
	}
	mustCheckClean(t, tgt)
}

// TestCommitStagingFailureChangesNothing: a failure while staging temp
// files must abort with the working tree, lock, and temp state untouched
// (§31 two-phase write).
func TestCommitStagingFailureChangesNothing(t *testing.T) {
	setEnv(t)
	src, shaA, tgt := appliedTarget(t)
	advanceToB(t, src)
	before := snapshot(t, tgt)

	calls := 0
	stageHook = func() error {
		calls++
		if calls == 2 {
			return errors.New("injected staging failure")
		}
		return nil
	}
	defer func() { stageHook = nil }()

	var out bytes.Buffer
	err := Update(tgt, &out, false)
	if err != ErrUpdateFailed || !strings.Contains(out.String(), "No files changed.") {
		t.Fatalf("err = %v, output:\n%s", err, out.String())
	}
	if calls != 2 {
		t.Fatalf("stageHook calls = %d, want the failure to abort staging", calls)
	}
	// No file changed and no stray temp file survived (snapshot walks
	// every file under the worktree).
	sameSnapshot(t, before, snapshot(t, tgt))
	if got := readFile(t, tgt, ".git-scaffold/lock"); got != shaA+"\n" {
		t.Fatalf("aborted staging advanced lock: %q", got)
	}

	// With the seam released the same update succeeds.
	stageHook = nil
	if err := Update(tgt, io.Discard, false); err != nil {
		t.Fatal(err)
	}
	mustCheckClean(t, tgt)
}

func TestUnifiedDiff(t *testing.T) {
	got := unifiedDiff("f.txt", []byte("a\nb\nc\n"), []byte("a\nx\nc\n"), true)
	want := "--- a/f.txt\n+++ b/f.txt\n@@ -1,3 +1,3 @@\n a\n-b\n+x\n c\n"
	if got != want {
		t.Fatalf("diff = %q, want %q", got, want)
	}
	if d := unifiedDiff("f", []byte("same\n"), []byte("same\n"), true); d != "" {
		t.Fatalf("diff of equal content = %q", d)
	}
	got = unifiedDiff("gone", []byte("only\n"), nil, false)
	if !strings.Contains(got, "+++ /dev/null") || !strings.Contains(got, "@@ -1 +0,0 @@") {
		t.Fatalf("missing-file diff = %q", got)
	}
	got = unifiedDiff("n", []byte("x"), []byte("y"), true)
	if !strings.Contains(got, "\\ No newline at end of file") {
		t.Fatalf("no-newline diff = %q", got)
	}
	got = unifiedDiff("bin", []byte("a\x00b"), []byte("c"), true)
	if !strings.Contains(got, "Binary files") {
		t.Fatalf("binary diff = %q", got)
	}
}

// TestShort: full SHAs abbreviate to 7 characters for display; anything
// already short passes through unchanged.
func TestShort(t *testing.T) {
	if got := short("0123456789abcdef0123456789abcdef01234567"); got != "0123456" {
		t.Fatalf("short(full sha) = %q", got)
	}
	if got := short("abc"); got != "abc" {
		t.Fatalf("short(\"abc\") = %q", got)
	}
	if got := short(""); got != "" {
		t.Fatalf("short(\"\") = %q", got)
	}
}
