// Package engine implements the git-scaffold commands over the pure
// materialization core: locate the worktree, load target configuration and
// lock, reconstruct expected trees via the source cache, and mutate the
// working tree transactionally (§31). All state changes are staged and
// validated in memory before anything is written; the write phase then
// stages every new file content as a temp file before any rename, so the
// residual risk is confined to a crash during the final rename/delete
// phase (see commit).
package engine

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/stephenc/git-scaffold/internal/config"
	"github.com/stephenc/git-scaffold/internal/gitx"
	"github.com/stephenc/git-scaffold/internal/materialize"
)

const (
	metaDir    = ".git-scaffold"
	configName = metaDir + "/config.toml"
	lockName   = metaDir + "/lock"
)

type target struct {
	root string
	cfg  *config.TargetConfig
	src  *gitx.Source
}

func load(dir string) (*target, error) {
	root, err := gitx.WorktreeRoot(dir)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(root, metaDir, "config.toml"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no scaffold configuration: %s not found", configName)
		}
		return nil, err
	}
	cfg, err := config.ParseTarget(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", configName, err)
	}
	src, err := gitx.Open(cfg.SourceGit)
	if err != nil {
		return nil, err
	}
	return &target{root: root, cfg: cfg, src: src}, nil
}

// lockRe enforces §7: the complete lock contents are one full SHA plus \n.
var lockRe = regexp.MustCompile(`^[0-9a-f]{40}\n$`)

// readLock returns the locked SHA, or "" when no lock exists yet.
func readLock(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, metaDir, "lock"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	if !lockRe.Match(data) {
		return "", fmt.Errorf("%s: malformed lock file (expected one full commit SHA)", lockName)
	}
	return string(data[:40]), nil
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// materializeAt reconstructs the expected managed tree at a source commit,
// loading patch files from disk. It fetches only if the commit is missing
// from the cache.
func (t *target) materializeAt(sha string) (map[string][]byte, error) {
	patches, err := t.loadPatches()
	if err != nil {
		return nil, err
	}
	return t.materializeWith(sha, patches)
}

// materializeWith is materializeAt with the patch files supplied in memory,
// keyed by path relative to .git-scaffold/. init --existing uses it to
// verify generated patches before anything is written.
func (t *target) materializeWith(sha string, patches map[string][]byte) (map[string][]byte, error) {
	if err := t.src.EnsureCommit(sha); err != nil {
		return nil, err
	}
	tree, err := t.src.ReadTree(sha)
	if err != nil {
		return nil, err
	}
	dd, ok := tree[configName]
	if !ok {
		return nil, fmt.Errorf("source commit %s has no scaffold descriptor %s", short(sha), configName)
	}
	desc, err := config.ParseDescriptor(dd)
	if err != nil {
		return nil, fmt.Errorf("source descriptor at %s: %w", short(sha), err)
	}
	out, err := materialize.Materialize(tree, desc, t.cfg, patches)
	if err != nil {
		return nil, err
	}
	// Remote descriptors are untrusted (§48): never let a managed path
	// overwrite scaffold metadata.
	for p := range out {
		if p == metaDir || strings.HasPrefix(p, metaDir+"/") {
			return nil, fmt.Errorf("source descriptor manages %q inside %s; refusing", p, metaDir)
		}
	}
	return out, nil
}

func (t *target) loadPatches() (map[string][]byte, error) {
	files := map[string][]byte{}
	for _, o := range t.cfg.Overrides {
		for _, p := range o.Patches {
			if _, ok := files[p]; ok {
				continue
			}
			data, err := os.ReadFile(filepath.Join(t.root, metaDir, filepath.FromSlash(p)))
			if err != nil {
				return nil, fmt.Errorf("patch file %s/%s: %w", metaDir, p, err)
			}
			files[p] = data
		}
	}
	return files, nil
}

// readWorking reads a managed path from the working tree; ok is false when
// the file does not exist. Reads go through the same symlink refusal as
// writes (§48): a symlinked managed path or symlinked ancestor within the
// repo is an error, never followed.
func readWorking(root, rel string) (data []byte, ok bool, err error) {
	abs, err := resolvePath(root, rel)
	if err != nil {
		return nil, false, err
	}
	data, err = os.ReadFile(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("%s: %w", rel, err)
	}
	return data, true, nil
}

// resolvePath resolves the on-disk location of a repo-relative managed path,
// refusing symlinked ancestors that alias outside the worktree and symlinked
// final paths (§48). Reads and writes alike must go through it: materialized
// managed files are always regular files, so a symlink anywhere on the way
// can never be correct content and must not be followed.
func resolvePath(root, rel string) (string, error) {
	abs := filepath.Join(root, filepath.FromSlash(rel))
	dir := filepath.Dir(abs)
	for {
		if _, err := os.Lstat(dir); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("%s: %w", rel, err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	if resolvedDir != resolvedRoot &&
		!strings.HasPrefix(resolvedDir, resolvedRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("managed path %q escapes the worktree via a symlinked directory", rel)
	}
	if fi, err := os.Lstat(abs); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("managed path %q is a symlink; managed files are regular files, refusing to follow it", rel)
	}
	return abs, nil
}

// conflicts returns managed paths whose working-tree content is neither the
// old expected materialization nor the new target content, i.e. unrelated
// local modifications that would be destroyed (§49). old may be nil when no
// prior materialization exists; a delete has no new content.
func conflicts(root string, writes map[string][]byte, deletes []string, old map[string][]byte) ([]string, error) {
	var out []string
	check := func(path string, hasNew bool, newContent []byte) error {
		w, ok, err := readWorking(root, path)
		if err != nil || !ok {
			return err
		}
		if hasNew && bytes.Equal(w, newContent) {
			return nil
		}
		if oldContent, ok := old[path]; ok && bytes.Equal(w, oldContent) {
			return nil
		}
		out = append(out, path)
		return nil
	}
	for _, p := range sortedKeys(writes) {
		if err := check(p, true, writes[p]); err != nil {
			return nil, err
		}
	}
	for _, p := range deletes {
		if err := check(p, false, nil); err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}

func conflictError(paths []string) error {
	return fmt.Errorf(
		"local modifications would be overwritten (rerun with --force to discard them):\n  %s",
		strings.Join(paths, "\n  "))
}

// stageHook is a test seam invoked after each successful staging step of
// commit; a non-nil error simulates a mid-staging I/O failure.
var stageHook func() error

// commit performs the write phase in two stages (§31). Stage one resolves
// every destination, pre-creates directories, and writes ALL new file
// contents (lock included) as temp files in the same directory as their
// final destination, synced to disk — same directory, so the later rename
// is same-volume and atomic. Only after every temp file exists does stage
// two perform the renames, then the deletions with empty-dir pruning, and
// the lock rename last. A failure during staging aborts cleanly: the temp
// files are removed and nothing else has been touched. A failure (or
// crash) during the rename/delete phase is the narrow residual window of
// §31 — renames of fully staged content on one volume, overwhelmingly
// unlikely to fail — and is the only point where a partial update can be
// left behind.
func commit(root string, writes map[string][]byte, deletes []string, lockSHA string) error {
	dests := map[string]string{}
	for _, p := range sortedKeys(writes) {
		abs, err := resolvePath(root, p)
		if err != nil {
			return err
		}
		dests[p] = abs
	}
	delDests := map[string]string{}
	for _, p := range deletes {
		abs, err := resolvePath(root, p)
		if err != nil {
			return err
		}
		delDests[p] = abs
	}
	for _, abs := range dests {
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return err
		}
	}
	if lockSHA != "" {
		if err := os.MkdirAll(filepath.Join(root, metaDir), 0o755); err != nil {
			return err
		}
	}

	// Stage phase: nothing but temp files is created here.
	order := sortedKeys(writes)
	tmps := map[string]string{}
	abort := func(err error) error {
		for _, tmp := range tmps {
			os.Remove(tmp)
		}
		return err
	}
	stage := func(key, abs string, content []byte) error {
		tmp, err := stageFile(abs, content)
		if err != nil {
			return err
		}
		tmps[key] = tmp
		if stageHook != nil {
			return stageHook()
		}
		return nil
	}
	for _, p := range order {
		if err := stage(p, dests[p], writes[p]); err != nil {
			return abort(err)
		}
	}
	// The key cannot collide with a repo-relative path.
	const lockKey = "\x00lock"
	lockAbs := filepath.Join(root, metaDir, "lock")
	if lockSHA != "" {
		if err := stage(lockKey, lockAbs, []byte(lockSHA+"\n")); err != nil {
			return abort(err)
		}
	}

	// Rename/delete phase: the §31 residual crash window starts here.
	for _, p := range order {
		if err := os.Rename(tmps[p], dests[p]); err != nil {
			return abort(err)
		}
		delete(tmps, p)
	}
	for _, p := range deletes {
		if err := os.Remove(delDests[p]); err != nil && !os.IsNotExist(err) {
			return abort(err)
		}
		removeEmptyParents(root, delDests[p])
	}
	if lockSHA != "" {
		if err := os.Rename(tmps[lockKey], lockAbs); err != nil {
			return abort(err)
		}
		delete(tmps, lockKey)
	}
	return nil
}

// stageFile writes content as a synced temp file in the directory of abs
// and returns the temp file's path; the caller renames or removes it.
func stageFile(abs string, content []byte) (string, error) {
	tmp, err := os.CreateTemp(filepath.Dir(abs), ".git-scaffold-*")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	_, werr := tmp.Write(content)
	if werr == nil {
		werr = tmp.Sync()
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(name)
		return "", werr
	}
	return name, nil
}

func writeFileAtomic(abs string, content []byte) error {
	tmp, err := stageFile(abs, content)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, abs); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// removeEmptyParents prunes directories left empty by a deletion, stopping at
// the worktree root.
func removeEmptyParents(root, abs string) {
	dir := filepath.Dir(abs)
	for dir != root && strings.HasPrefix(dir, root+string(filepath.Separator)) {
		if os.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// status renders a bold summary line in the style of the sibling gits tool:
// "✅️ msg" on success, "❌ msg" on failure.
func status(ok bool, msg string) string {
	mark := "✅️"
	if !ok {
		mark = "❌"
	}
	return "\033[1m" + mark + " " + msg + "\033[0m\n"
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
