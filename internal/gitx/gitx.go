// Package gitx shells out to the installed git binary (§6: any Git URL
// syntax git supports is accepted): worktree discovery, remote ref
// resolution, and a bare-repository cache of scaffold sources (§50). The
// cache location is $GIT_SCAFFOLD_CACHE, else the global config cache-dir,
// else $XDG_CACHE_HOME/git-scaffold, else ~/.cache/git-scaffold (§56),
// keyed by a hash of the source URL. The cache never affects output
// determinism: it only stores immutable Git objects.
package gitx

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/stephenc/git-scaffold/internal/globalcfg"
)

func run(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// Never hang on credential prompts; failures should be immediate.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", args[0], msg)
	}
	return out.Bytes(), nil
}

// Git runs an arbitrary git command in dir with optional stdin content and
// extra environment entries, returning trimmed stdout. It exists for callers
// (the proposal flow, §39-§45) that drive git plumbing in the target
// repository rather than the source cache.
func Git(dir string, stdin []byte, extraEnv []string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"), extraEnv...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimSpace(out.String()), nil
}

// WorktreeRoot resolves the root of the Git worktree containing dir (§49:
// commands may run from subdirectories).
func WorktreeRoot(dir string) (string, error) {
	out, err := run(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("not inside a Git worktree: %w", err)
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return "", fmt.Errorf("not inside a Git worktree")
	}
	return filepath.FromSlash(root), nil
}

func cacheRoot() (string, error) {
	return globalcfg.CacheDir()
}

// Source is a scaffold source repository backed by a bare cache repository.
type Source struct {
	url string
	dir string
}

// Open ensures the bare cache repository for url exists and returns it. No
// network access occurs.
func Open(url string) (*Source, error) {
	root, err := cacheRoot()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(url))
	dir := filepath.Join(root, hex.EncodeToString(sum[:])+".git")
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		if _, err := run("", "init", "--bare", "--quiet", dir); err != nil {
			return nil, err
		}
	}
	return &Source{url: url, dir: dir}, nil
}

// URL returns the configured source URL.
func (s *Source) URL() string { return s.url }

// HasCommit reports whether sha resolves to a commit already in the cache.
func (s *Source) HasCommit(sha string) bool {
	_, err := run(s.dir, "cat-file", "-e", sha+"^{commit}")
	return err == nil
}

// Fetch updates the cache with all heads and tags of the source.
func (s *Source) Fetch() error {
	if _, err := run(s.dir, "fetch", "--force", "--prune", "--quiet", s.url,
		"+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*"); err != nil {
		return fmt.Errorf("cannot fetch source %s: %w", s.url, err)
	}
	return nil
}

// EnsureCommit makes sha available in the cache, fetching only if it is
// missing (§50: a locally available locked commit needs no network).
func (s *Source) EnsureCommit(sha string) error {
	if s.HasCommit(sha) {
		return nil
	}
	if err := s.Fetch(); err != nil {
		return err
	}
	if s.HasCommit(sha) {
		return nil
	}
	// Servers permitting arbitrary-SHA fetches can serve commits no longer
	// reachable from any advertised ref.
	_, _ = run(s.dir, "fetch", "--force", "--quiet", s.url, sha)
	if s.HasCommit(sha) {
		return nil
	}
	return fmt.Errorf("commit %s is not available from %s", sha, s.url)
}

var hexRe = regexp.MustCompile(`^[0-9a-f]{4,40}$`)

// ResolveRef resolves ref against the remote (ls-remote); an empty ref means
// the remote's HEAD (§6). Refs not advertised remotely (e.g. commit SHAs)
// fall back to local resolution after a fetch.
func (s *Source) ResolveRef(ref string) (string, error) {
	refs, lsErr := s.lsRemote()
	if lsErr == nil {
		if ref == "" {
			if sha, ok := refs["HEAD"]; ok {
				return sha, nil
			}
			return "", fmt.Errorf("source %s advertises no HEAD", s.url)
		}
		// Annotated tags resolve through their peeled ^{} entry so the lock
		// always records a commit.
		candidates := []string{}
		if strings.HasPrefix(ref, "refs/") {
			candidates = append(candidates, ref+"^{}", ref)
		}
		candidates = append(candidates,
			"refs/heads/"+ref, "refs/tags/"+ref+"^{}", "refs/tags/"+ref)
		for _, c := range candidates {
			if sha, ok := refs[c]; ok {
				return sha, nil
			}
		}
	}
	if ref == "" {
		return "", fmt.Errorf("cannot resolve source %s: %w", s.url, lsErr)
	}
	if !(hexRe.MatchString(ref) && s.HasCommit(ref)) {
		if err := s.Fetch(); err != nil {
			if lsErr != nil {
				return "", fmt.Errorf("cannot resolve source %s: %w", s.url, lsErr)
			}
			return "", err
		}
	}
	out, err := run(s.dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("cannot resolve ref %q in %s", ref, s.url)
	}
	return strings.TrimSpace(string(out)), nil
}

func (s *Source) lsRemote() (map[string]string, error) {
	out, err := run(s.dir, "ls-remote", "--quiet", s.url)
	if err != nil {
		return nil, err
	}
	refs := map[string]string{}
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		sha, name, ok := strings.Cut(line, "\t")
		if ok && sha != "" {
			refs[name] = sha
		}
	}
	return refs, nil
}

// ReadTree loads the full tree of a commit into memory as repository-relative
// `/`-separated paths. Only regular-file blobs are included (§9: symlinks and
// submodules are never materialized).
func (s *Source) ReadTree(sha string) (map[string][]byte, error) {
	out, err := run(s.dir, "ls-tree", "-r", "-z", sha)
	if err != nil {
		return nil, err
	}
	type entry struct{ path, obj string }
	var entries []entry
	for _, rec := range strings.Split(string(out), "\x00") {
		if rec == "" {
			continue
		}
		head, path, ok := strings.Cut(rec, "\t")
		if !ok {
			return nil, fmt.Errorf("unexpected ls-tree output: %q", rec)
		}
		f := strings.Fields(head)
		if len(f) != 3 {
			return nil, fmt.Errorf("unexpected ls-tree output: %q", rec)
		}
		if f[1] != "blob" || (f[0] != "100644" && f[0] != "100755") {
			continue
		}
		entries = append(entries, entry{path: path, obj: f[2]})
	}

	tree := make(map[string][]byte, len(entries))
	if len(entries) == 0 {
		return tree, nil
	}
	var in bytes.Buffer
	for _, e := range entries {
		in.WriteString(e.obj)
		in.WriteByte('\n')
	}
	cmd := exec.Command("git", "cat-file", "--batch")
	cmd.Dir = s.dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.Stdin = &in
	var errb bytes.Buffer
	cmd.Stderr = &errb
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git cat-file: %s", strings.TrimSpace(errb.String()))
	}
	pos := 0
	for _, e := range entries {
		nl := bytes.IndexByte(raw[pos:], '\n')
		if nl < 0 {
			return nil, fmt.Errorf("truncated cat-file output")
		}
		header := string(raw[pos : pos+nl])
		pos += nl + 1
		f := strings.Fields(header)
		if len(f) != 3 || f[1] != "blob" {
			return nil, fmt.Errorf("cannot read %s at %s: %s", e.path, sha, header)
		}
		size, err := strconv.Atoi(f[2])
		if err != nil || pos+size+1 > len(raw) {
			return nil, fmt.Errorf("truncated cat-file output for %s", e.path)
		}
		tree[e.path] = append([]byte(nil), raw[pos:pos+size]...)
		pos += size + 1 // content plus trailing newline
	}
	return tree, nil
}
