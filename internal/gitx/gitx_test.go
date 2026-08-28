package gitx

import (
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

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commitAll(t *testing.T, dir, msg string) string {
	t.Helper()
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", msg)
	return runGit(t, dir, "rev-parse", "HEAD")
}

// cacheRepos lists the bare cache repositories under GIT_SCAFFOLD_CACHE.
func cacheRepos(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(os.Getenv("GIT_SCAFFOLD_CACHE"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestOpenCreatesAndReusesCache(t *testing.T) {
	setEnv(t)
	src := newRepo(t)
	s, err := Open(src)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := s.URL(); got != src {
		t.Fatalf("URL() = %q, want %q", got, src)
	}
	repos := cacheRepos(t)
	if len(repos) != 1 || !strings.HasSuffix(repos[0], ".git") {
		t.Fatalf("cache repos after Open = %v, want one *.git", repos)
	}
	// The cache entry is a bare repository, keyed by URL: reopening the same
	// URL reuses it, a different URL gets its own.
	if _, err := os.Stat(filepath.Join(os.Getenv("GIT_SCAFFOLD_CACHE"), repos[0], "HEAD")); err != nil {
		t.Fatalf("cache entry is not a bare repository: %v", err)
	}
	if _, err := Open(src); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := cacheRepos(t); len(got) != 1 {
		t.Fatalf("reopen created a second cache entry: %v", got)
	}
	if _, err := Open(newRepo(t)); err != nil {
		t.Fatalf("Open other: %v", err)
	}
	if got := cacheRepos(t); len(got) != 2 {
		t.Fatalf("distinct URLs must get distinct cache entries: %v", got)
	}
}

func TestResolveRef(t *testing.T) {
	setEnv(t)
	src := newRepo(t)
	writeFile(t, src, "a.txt", "A\n")
	shaA := commitAll(t, src, "A")
	runGit(t, src, "tag", "-a", "-m", "release", "v1")
	runGit(t, src, "checkout", "-q", "-b", "dev")
	writeFile(t, src, "b.txt", "B\n")
	shaB := commitAll(t, src, "B")
	runGit(t, src, "checkout", "-q", "main")

	s, err := Open(src)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for name, tc := range map[string]struct {
		ref  string
		want string
	}{
		"branch":        {"dev", shaB},
		"default HEAD":  {"", shaA},
		"annotated tag": {"v1", shaA}, // peeled to the commit, not the tag object
		"full ref":      {"refs/heads/dev", shaB},
		"commit sha":    {shaA, shaA},
	} {
		got, err := s.ResolveRef(tc.ref)
		if err != nil {
			t.Fatalf("%s: ResolveRef(%q): %v", name, tc.ref, err)
		}
		if got != tc.want {
			t.Errorf("%s: ResolveRef(%q) = %s, want %s", name, tc.ref, got, tc.want)
		}
	}

	if _, err := s.ResolveRef("no-such-ref"); err == nil ||
		!strings.Contains(err.Error(), "no-such-ref") {
		t.Fatalf("nonexistent ref: err = %v", err)
	}
}

func TestEnsureCommitFetchesWhenMissing(t *testing.T) {
	setEnv(t)
	src := newRepo(t)
	writeFile(t, src, "a.txt", "A\n")
	shaA := commitAll(t, src, "A")

	s, err := Open(src)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s.HasCommit(shaA) {
		t.Fatal("fresh cache claims to have the commit")
	}
	if err := s.EnsureCommit(shaA); err != nil {
		t.Fatalf("EnsureCommit(A): %v", err)
	}
	if !s.HasCommit(shaA) {
		t.Fatal("commit missing from cache after EnsureCommit")
	}

	// A commit appearing upstream after the cache was populated: EnsureCommit
	// must fetch again rather than trust the stale cache.
	writeFile(t, src, "b.txt", "B\n")
	shaB := commitAll(t, src, "B")
	if s.HasCommit(shaB) {
		t.Fatal("stale cache claims to have the new upstream commit")
	}
	if err := s.EnsureCommit(shaB); err != nil {
		t.Fatalf("EnsureCommit(B): %v", err)
	}
	if !s.HasCommit(shaB) {
		t.Fatal("new upstream commit missing after EnsureCommit")
	}

	// An already-cached commit needs no network; and a commit that exists
	// nowhere is a hard error naming the source.
	if err := s.EnsureCommit(shaA); err != nil {
		t.Fatalf("EnsureCommit cached: %v", err)
	}
	bogus := strings.Repeat("deadbeef", 5)
	err = s.EnsureCommit(bogus)
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("EnsureCommit(bogus): err = %v", err)
	}
}

func TestReadTree(t *testing.T) {
	setEnv(t)
	src := newRepo(t)
	writeFile(t, src, "a.txt", "hello\n")
	writeFile(t, src, "dir/b.sh", "#!/bin/sh\necho ok\n")
	if runtime.GOOS != "windows" {
		if err := os.Chmod(filepath.Join(src, "dir", "b.sh"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("a.txt", filepath.Join(src, "link")); err != nil {
			t.Fatal(err)
		}
	}
	sha := commitAll(t, src, "A")
	// A submodule entry (gitlink), added via plumbing so no real submodule is
	// needed; §9: never materialized.
	runGit(t, src, "update-index", "--add", "--cacheinfo", "160000,"+sha+",vendor/dep")
	runGit(t, src, "commit", "-q", "-m", "with gitlink")
	sha = runGit(t, src, "rev-parse", "HEAD")

	s, err := Open(src)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.EnsureCommit(sha); err != nil {
		t.Fatalf("EnsureCommit: %v", err)
	}
	tree, err := s.ReadTree(sha)
	if err != nil {
		t.Fatalf("ReadTree: %v", err)
	}
	if got := string(tree["a.txt"]); got != "hello\n" {
		t.Fatalf("a.txt = %q", got)
	}
	if got := string(tree["dir/b.sh"]); got != "#!/bin/sh\necho ok\n" {
		t.Fatalf("dir/b.sh = %q", got)
	}
	if _, ok := tree["vendor/dep"]; ok {
		t.Fatal("submodule gitlink materialized")
	}
	if runtime.GOOS != "windows" {
		if _, ok := tree["link"]; ok {
			t.Fatal("symlink materialized")
		}
		if len(tree) != 2 {
			t.Fatalf("tree = %v, want exactly a.txt and dir/b.sh", tree)
		}
	}
}

func TestWorktreeRoot(t *testing.T) {
	setEnv(t)
	repo := newRepo(t)
	sub := filepath.Join(repo, "deep", "inside")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := WorktreeRoot(sub)
	if err != nil {
		t.Fatalf("WorktreeRoot: %v", err)
	}
	// macOS temp dirs sit behind a /var → /private/var symlink; compare
	// resolved paths.
	wantR, _ := filepath.EvalSymlinks(repo)
	gotR, _ := filepath.EvalSymlinks(got)
	if gotR != wantR {
		t.Fatalf("WorktreeRoot = %q, want %q", got, repo)
	}

	outside := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", outside)
	if _, err := WorktreeRoot(outside); err == nil ||
		!strings.Contains(err.Error(), "not inside a Git worktree") {
		t.Fatalf("outside a repo: err = %v", err)
	}
}

// TestFetchUnreachableSource: a source that no longer exists is a hard,
// named error from Fetch — and EnsureCommit propagates it.
func TestFetchUnreachableSource(t *testing.T) {
	setEnv(t)
	gone := filepath.Join(t.TempDir(), "no-such-repo")
	s, err := Open(gone)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Fetch(); err == nil || !strings.Contains(err.Error(), "cannot fetch source "+gone) {
		t.Fatalf("Fetch: err = %v", err)
	}
	sha := strings.Repeat("deadbeef", 5)
	if err := s.EnsureCommit(sha); err == nil || !strings.Contains(err.Error(), "cannot fetch source "+gone) {
		t.Fatalf("EnsureCommit: err = %v", err)
	}
}
