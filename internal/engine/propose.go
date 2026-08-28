package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stephenc/git-scaffold/internal/gitx"
)

// DefaultProposalBranch is the tool-owned branch used by propose (§40).
const DefaultProposalBranch = "git-scaffold/update"

// proposalTitle is both the proposal commit subject (§41) and the PR title.
const proposalTitle = "chore: update repository scaffold"

// lookPath is a test seam so tests can simulate `gh` being absent (or
// present as a recorder) without manipulating PATH.
var lookPath = exec.LookPath

// Propose implements `git scaffold propose` (§39-§45): resolve the configured
// ref like update, and if it has advanced past the lock, publish the update
// as a commit on the tool-owned proposal branch, push it, and ensure a PR
// exists.
//
// Working-tree safety: propose never runs the update against the working
// tree. The proposal commit is built entirely with git plumbing — materialized
// contents become blobs via `git hash-object -w`, a temporary index
// (GIT_INDEX_FILE) is seeded with `git read-tree` from the base commit and
// edited with `git update-index`, then `git write-tree`/`git commit-tree`/
// `git update-ref` produce the branch. The user's branch, index, working
// tree, and lock file are left untouched; the proposal commit itself carries
// the new lock content and is the delivery vehicle for the update.
func Propose(dir string, out io.Writer, branch string) error {
	if branch == "" {
		branch = DefaultProposalBranch
	}
	t, err := load(dir)
	if err != nil {
		return err
	}
	oldSHA, err := readLock(t.root)
	if err != nil {
		return err
	}
	if oldSHA == "" {
		return fmt.Errorf("no %s; run `git scaffold apply` first", lockName)
	}
	newSHA, err := t.src.ResolveRef(t.cfg.SourceRef)
	if err != nil {
		return err
	}
	ref := t.cfg.SourceRef
	if ref == "" {
		ref = "HEAD"
	}
	if newSHA == oldSHA {
		io.WriteString(out, status(true, "scaffold is up to date at "+short(oldSHA)+"; nothing to propose"))
		return nil
	}

	// The raw configured URL (not the insteadOf-rewritten transport URL)
	// determines the hosting provider; its absence means nowhere to push.
	originURL, err := gitx.Git(t.root, nil, nil, "config", "--get", "remote.origin.url")
	if err != nil || originURL == "" {
		return fmt.Errorf("no \"origin\" remote configured; propose has nowhere to push the proposal branch")
	}
	// update-ref on the checked-out branch would desync HEAD from the
	// index/working tree, so refuse that arrangement outright (§49).
	if head, err := gitx.Git(t.root, nil, nil, "symbolic-ref", "--quiet", "HEAD"); err == nil && head == "refs/heads/"+branch {
		return fmt.Errorf("proposal branch %q is currently checked out; switch branches and rerun", branch)
	}

	fmt.Fprintf(out, "Proposing scaffold update\n\nsource: %s\nref:    %s\n\n%s → %s\n\n",
		t.cfg.SourceGit, ref, short(oldSHA), short(newSHA))

	oldTree, err := t.materializeAt(oldSHA)
	if err != nil {
		return err
	}
	newTree, err := t.materializeAt(newSHA)
	if err != nil {
		return err
	}
	var added, modified, deleted []string
	for _, p := range sortedKeys(newTree) {
		oldContent, existed := oldTree[p]
		switch {
		case !existed:
			added = append(added, p)
		case !bytes.Equal(oldContent, newTree[p]):
			modified = append(modified, p)
		}
	}
	for _, p := range sortedKeys(oldTree) {
		if _, ok := newTree[p]; !ok {
			deleted = append(deleted, p)
		}
	}
	for _, p := range modified {
		fmt.Fprintf(out, "M %s\n", p)
	}
	for _, p := range added {
		fmt.Fprintf(out, "A %s\n", p)
	}
	for _, p := range deleted {
		fmt.Fprintf(out, "D %s\n", p)
	}

	base, err := proposalBase(t.root)
	if err != nil {
		return err
	}
	treeSHA, err := buildProposalTree(t.root, base, newTree, deleted, newSHA)
	if err != nil {
		return err
	}

	// §45 idempotency: when the remote branch already carries exactly this
	// tree, skip the push and PR churn entirely.
	remoteSHA := lsRemoteBranch(t.root, branch)
	if remoteSHA != "" {
		if _, err := gitx.Git(t.root, nil, nil, "cat-file", "-e", remoteSHA+"^{commit}"); err != nil {
			// Best effort: make the remote tip's tree comparable locally.
			_, _ = gitx.Git(t.root, nil, nil, "fetch", "--quiet", "origin", "refs/heads/"+branch)
		}
		if rt, err := gitx.Git(t.root, nil, nil, "rev-parse", "--verify", "--quiet", remoteSHA+"^{tree}"); err == nil && rt == treeSHA {
			io.WriteString(out, status(true, fmt.Sprintf(
				"proposal branch %s already carries %s → %s; nothing to do", branch, short(oldSHA), short(newSHA))))
			return nil
		}
	}

	msg := proposalTitle + "\n\nGit-Scaffold-Source: " + newSHA + "\n"
	commitSHA, err := gitx.Git(t.root, []byte(msg), nil, "commit-tree", treeSHA, "-p", base)
	if err != nil {
		return err
	}
	if _, err := gitx.Git(t.root, nil, nil, "update-ref", "refs/heads/"+branch, commitSHA); err != nil {
		return err
	}
	// §40: safe force semantics. The lease pins the exact remote tip we
	// observed ("" = the branch must not exist yet).
	lease := "--force-with-lease=refs/heads/" + branch + ":" + remoteSHA
	if _, err := gitx.Git(t.root, nil, nil, "push", "--quiet", lease,
		"origin", "refs/heads/"+branch+":refs/heads/"+branch); err != nil {
		return err
	}
	fmt.Fprintf(out, "\npushed %s to origin\n", branch)

	tmpDir, err := os.MkdirTemp("", "git-scaffold-propose-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	bodyFile := filepath.Join(tmpDir, "body.md")
	body := proposalBody(t.cfg.SourceGit, ref, oldSHA, newSHA, modified, added, deleted, appliedPatches(t))
	if err := os.WriteFile(bodyFile, []byte(body), 0o600); err != nil {
		return err
	}

	if err := ensureProposal(t.root, out, t.cfg.ProposeCreateCommand, originURL, branch, bodyFile); err != nil {
		return err
	}
	io.WriteString(out, status(true, fmt.Sprintf(
		"proposed scaffold update %s → %s on %s", short(oldSHA), short(newSHA), branch)))
	return nil
}

// proposalBase picks the commit the proposal builds on: the remote default
// branch head when discoverable (fetching it if needed), else the local HEAD.
func proposalBase(root string) (string, error) {
	if out, err := gitx.Git(root, nil, nil, "ls-remote", "--symref", "origin", "HEAD"); err == nil {
		var refName, sha string
		for _, line := range strings.Split(out, "\n") {
			left, right, ok := strings.Cut(line, "\t")
			if !ok || right != "HEAD" {
				continue
			}
			if target, isSym := strings.CutPrefix(left, "ref: "); isSym {
				refName = target
			} else if left != "" {
				sha = left
			}
		}
		if sha != "" {
			if _, err := gitx.Git(root, nil, nil, "cat-file", "-e", sha+"^{commit}"); err != nil {
				fetchArg := sha
				if refName != "" {
					fetchArg = refName
				}
				_, _ = gitx.Git(root, nil, nil, "fetch", "--quiet", "origin", fetchArg)
			}
			if _, err := gitx.Git(root, nil, nil, "cat-file", "-e", sha+"^{commit}"); err == nil {
				return sha, nil
			}
		}
	}
	sha, err := gitx.Git(root, nil, nil, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err != nil {
		return "", fmt.Errorf("cannot determine a base commit for the proposal (no remote default branch and no local HEAD)")
	}
	return sha, nil
}

// buildProposalTree assembles the proposal tree in a temporary index without
// touching the real index or working tree: base tree + full new
// materialization + deletions + the new lock content.
func buildProposalTree(root, base string, newTree map[string][]byte, deleted []string, newSHA string) (string, error) {
	tmpDir, err := os.MkdirTemp("", "git-scaffold-index-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(tmpDir, "index")}
	if _, err := gitx.Git(root, nil, env, "read-tree", base); err != nil {
		return "", err
	}
	var spec bytes.Buffer
	writeEntry := func(mode, blob, path string) {
		spec.WriteString(mode + " " + blob + "\t" + path)
		spec.WriteByte(0)
	}
	for _, p := range sortedKeys(newTree) {
		blob, err := gitx.Git(root, newTree[p], nil, "hash-object", "-w", "--stdin")
		if err != nil {
			return "", err
		}
		writeEntry("100644", blob, p)
	}
	for _, p := range deleted {
		writeEntry("0", strings.Repeat("0", 40), p)
	}
	lockBlob, err := gitx.Git(root, []byte(newSHA+"\n"), nil, "hash-object", "-w", "--stdin")
	if err != nil {
		return "", err
	}
	writeEntry("100644", lockBlob, lockName)
	if _, err := gitx.Git(root, spec.Bytes(), env, "update-index", "-z", "--index-info"); err != nil {
		return "", err
	}
	return gitx.Git(root, nil, env, "write-tree")
}

// lsRemoteBranch returns the sha of refs/heads/<branch> on origin, or "".
func lsRemoteBranch(root, branch string) string {
	out, err := gitx.Git(root, nil, nil, "ls-remote", "--quiet", "origin", "refs/heads/"+branch)
	if err != nil {
		return ""
	}
	sha, _, ok := strings.Cut(out, "\t")
	if !ok {
		return ""
	}
	return sha
}

// proposalBody renders the §42 proposal description.
func proposalBody(source, ref, oldSHA, newSHA string, modified, added, deleted []string, patches []string) string {
	var b strings.Builder
	b.WriteString("Automated scaffold update proposed by `git scaffold propose`.\n\n")
	fmt.Fprintf(&b, "- Source: %s\n- Ref: %s\n- Old: %s\n- New: %s\n\n", source, ref, oldSHA, newSHA)
	b.WriteString("Changed managed files:\n\n```\n")
	for _, p := range modified {
		fmt.Fprintf(&b, "M %s\n", p)
	}
	for _, p := range added {
		fmt.Fprintf(&b, "A %s\n", p)
	}
	for _, p := range deleted {
		fmt.Fprintf(&b, "D %s\n", p)
	}
	b.WriteString("```\n\nApplied target patches:\n\n")
	if len(patches) == 0 {
		b.WriteString("- none\n")
	} else {
		for _, p := range patches {
			fmt.Fprintf(&b, "- %s\n", p)
		}
	}
	return b.String()
}

// appliedPatches lists the configured target patch files (relative to
// .git-scaffold/), ordered by override path then declaration order.
func appliedPatches(t *target) []string {
	var out []string
	seen := map[string]bool{}
	paths := make([]string, 0, len(t.cfg.Overrides))
	for p := range t.cfg.Overrides {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		for _, patch := range t.cfg.Overrides[p].Patches {
			if !seen[patch] {
				seen[patch] = true
				out = append(out, patch)
			}
		}
	}
	return out
}

// ensureProposal makes sure an open PR/MR exists for the pushed branch. A
// configured custom create-command (§44) replaces provider integration
// entirely; otherwise github.com uses `gh` (§43), and any other provider (or
// a missing `gh`) leaves the pushed branch with a notice — not an error.
func ensureProposal(root string, out io.Writer, createCommand []string, originURL, branch, bodyFile string) error {
	if len(createCommand) > 0 {
		argv := substituteCommand(createCommand, branch, bodyFile)
		if _, err := runTool(root, argv); err != nil {
			return fmt.Errorf("propose create-command: %w", err)
		}
		fmt.Fprintf(out, "ran configured create-command %q\n", argv[0])
		return nil
	}
	if providerHost(originURL) == "github.com" {
		if gh, err := lookPath("gh"); err == nil {
			return ghEnsurePR(root, out, gh, branch, bodyFile)
		}
	}
	fmt.Fprintf(out, "branch %s was pushed, but automatic PR creation is unavailable for this hosting provider; please open the proposal manually\n", branch)
	return nil
}

// substituteCommand replaces the §44 placeholders by literal string
// substitution within each argv element. Both the documented spaced form
// ("{{ branch }}") and the tighter "{{branch}}" are accepted.
func substituteCommand(argv []string, branch, bodyFile string) []string {
	r := strings.NewReplacer(
		"{{ branch }}", branch, "{{branch}}", branch,
		"{{ title }}", proposalTitle, "{{title}}", proposalTitle,
		"{{ body_file }}", bodyFile, "{{body_file}}", bodyFile,
	)
	out := make([]string, len(argv))
	for i, a := range argv {
		out[i] = r.Replace(a)
	}
	return out
}

// ghEnsurePR checks for an existing open PR for the branch and creates one
// if none exists, via the gh CLI (§43).
func ghEnsurePR(root string, out io.Writer, gh, branch, bodyFile string) error {
	listOut, err := runTool(root, []string{gh, "pr", "list", "--head", branch, "--state", "open", "--json", "number"})
	if err != nil {
		return fmt.Errorf("gh pr list: %w", err)
	}
	var prs []struct {
		Number int `json:"number"`
	}
	if err := json.Unmarshal([]byte(listOut), &prs); err != nil {
		return fmt.Errorf("gh pr list: unexpected output %q", listOut)
	}
	if len(prs) > 0 {
		fmt.Fprintf(out, "existing open PR #%d refreshed\n", prs[0].Number)
		return nil
	}
	if _, err := runTool(root, []string{gh, "pr", "create", "--head", branch, "--title", proposalTitle, "--body-file", bodyFile}); err != nil {
		return fmt.Errorf("gh pr create: %w", err)
	}
	fmt.Fprintf(out, "opened PR for %s\n", branch)
	return nil
}

// runTool executes an external command directly (no shell, §44) in root.
func runTool(root string, argv []string) (string, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s: %s", argv[0], msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// providerHost extracts the lowercase host of a remote URL for provider
// detection (§43). It understands scheme URLs and scp-like syntax; local
// paths yield "".
func providerHost(url string) string {
	if i := strings.Index(url, "://"); i >= 0 {
		rest := url[i+3:]
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			rest = rest[:j]
		}
		if at := strings.LastIndexByte(rest, '@'); at >= 0 {
			rest = rest[at+1:]
		}
		if j := strings.IndexByte(rest, ':'); j >= 0 {
			rest = rest[:j]
		}
		return strings.ToLower(rest)
	}
	// scp-like user@host:path (a colon before any slash and not a Windows
	// drive letter).
	colon := strings.IndexByte(url, ':')
	slash := strings.IndexByte(url, '/')
	if colon > 0 && (slash < 0 || colon < slash) {
		host := url[:colon]
		if at := strings.LastIndexByte(host, '@'); at >= 0 {
			host = host[at+1:]
		}
		if len(host) > 1 {
			return strings.ToLower(host)
		}
	}
	return ""
}
