package engine

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// proposeTarget builds appliedTarget, commits everything on main, and wires a
// local bare repository as the "origin" remote (the propose push target).
// extraConfig is appended to the target configuration before the initial
// apply (e.g. a [propose] section).
func proposeTarget(t *testing.T, extraConfig string) (srcDir, shaA, tgtDir, originDir string) {
	t.Helper()
	srcDir, shaA = sourceRepoA(t)
	tgtDir = newRepo(t)
	writeTargetConfig(t, tgtDir, srcDir)
	if extraConfig != "" {
		cfgPath := filepath.Join(tgtDir, ".git-scaffold", "config.toml")
		data, err := os.ReadFile(cfgPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cfgPath, append(data, []byte(extraConfig)...), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := Apply(tgtDir, &out, false); err != nil {
		t.Fatalf("initial apply: %v\n%s", err, out.String())
	}
	commitAll(t, tgtDir, "base")
	originDir = filepath.Join(t.TempDir(), "origin.git")
	runGit(t, tgtDir, "init", "-q", "--bare", "-b", "main", originDir)
	runGit(t, tgtDir, "remote", "add", "origin", originDir)
	runGit(t, tgtDir, "push", "-q", "origin", "main")
	return srcDir, shaA, tgtDir, originDir
}

// remoteBranchSHA returns the sha of the proposal branch on origin, or "".
func remoteBranchSHA(t *testing.T, tgtDir, branch string) string {
	t.Helper()
	out := runGit(t, tgtDir, "ls-remote", "origin", "refs/heads/"+branch)
	sha, _, _ := strings.Cut(out, "\t")
	return sha
}

func TestProposeNoUpdate(t *testing.T) {
	setEnv(t)
	_, _, tgt, origin := proposeTarget(t, "")
	before := snapshot(t, tgt)
	var out bytes.Buffer
	if err := Propose(tgt, &out, ""); err != nil {
		t.Fatalf("propose: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "nothing to propose") {
		t.Fatalf("output:\n%s", out.String())
	}
	if sha := remoteBranchSHA(t, tgt, DefaultProposalBranch); sha != "" {
		t.Fatalf("proposal branch created on origin: %s", sha)
	}
	if got := runGit(t, tgt, "for-each-ref", "refs/heads"); strings.Contains(got, DefaultProposalBranch) {
		t.Fatalf("local proposal branch created:\n%s", got)
	}
	// §45: no commit anywhere — origin still has only the single base commit.
	if got := runGit(t, origin, "rev-list", "--all", "--count"); got != "1" {
		t.Fatalf("origin commit count = %s", got)
	}
	sameSnapshot(t, before, snapshot(t, tgt))
}

func TestProposeWithUpdate(t *testing.T) {
	setEnv(t)
	src, shaA, tgt, origin := proposeTarget(t, "")
	shaB := advanceToB(t, src)
	mainSHA := runGit(t, origin, "rev-parse", "refs/heads/main")
	before := snapshot(t, tgt)

	var out bytes.Buffer
	if err := Propose(tgt, &out, ""); err != nil {
		t.Fatalf("propose: %v\n%s", err, out.String())
	}
	s := out.String()
	for _, want := range []string{
		short(shaA) + " → " + short(shaB),
		"M Makefile\n", "M go.mod\n", "M .golangci.yml\n",
		"A .github/workflows/security.yml\n",
		"D .github/workflows/test.yml\n",
		// A local-path origin is an unknown hosting provider (§43): pushed,
		// notice printed, no error.
		"automatic PR creation is unavailable",
		"proposed scaffold update",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("output missing %q:\n%s", want, s)
		}
	}

	branch := "refs/heads/" + DefaultProposalBranch
	// §41: commit message with trailer, on the branch, based on origin main.
	msg := runGit(t, origin, "log", "-1", "--format=%B", branch)
	if msg != "chore: update repository scaffold\n\nGit-Scaffold-Source: "+shaB {
		t.Fatalf("commit message = %q", msg)
	}
	if got := runGit(t, origin, "rev-parse", branch+"^"); got != mainSHA {
		t.Fatalf("proposal parent = %s, want origin main %s", got, mainSHA)
	}
	// The proposal tree delivers the update, including the new lock.
	if got := runGit(t, origin, "show", branch+":.git-scaffold/lock"); got != shaB {
		t.Fatalf("proposal lock = %q, want %q", got, shaB)
	}
	if got := runGit(t, origin, "show", branch+":Makefile"); !strings.Contains(got, "go test") {
		t.Fatalf("proposal Makefile = %q", got)
	}
	files := runGit(t, origin, "ls-tree", "-r", "--name-only", branch)
	if strings.Contains(files, ".github/workflows/test.yml") {
		t.Fatalf("deleted file still in proposal tree:\n%s", files)
	}
	if !strings.Contains(files, ".github/workflows/security.yml") {
		t.Fatalf("added file missing from proposal tree:\n%s", files)
	}
	if got := runGit(t, origin, "show", branch+":.golangci.yml"); !strings.Contains(got, "10m") {
		t.Fatalf("target patch not applied in proposal: %q", got)
	}

	// The user's branch, working tree, and lock are untouched (§39, §49).
	if got := runGit(t, tgt, "symbolic-ref", "HEAD"); got != "refs/heads/main" {
		t.Fatalf("HEAD moved to %s", got)
	}
	if got := runGit(t, tgt, "status", "--porcelain"); got != "" {
		t.Fatalf("working tree disturbed:\n%s", got)
	}
	sameSnapshot(t, before, snapshot(t, tgt))
	if got := readFile(t, tgt, ".git-scaffold/lock"); got != shaA+"\n" {
		t.Fatalf("user lock advanced: %q", got)
	}
}

func TestProposeRefreshAndIdempotency(t *testing.T) {
	setEnv(t)
	src, _, tgt, origin := proposeTarget(t, "")
	advanceToB(t, src)
	if err := Propose(tgt, io.Discard, ""); err != nil {
		t.Fatalf("first propose: %v", err)
	}
	tipB := remoteBranchSHA(t, tgt, DefaultProposalBranch)
	if tipB == "" {
		t.Fatal("no proposal branch after first propose")
	}

	// Idempotent re-run: no source change, no new commit, no branch churn.
	var out bytes.Buffer
	if err := Propose(tgt, &out, ""); err != nil {
		t.Fatalf("re-run propose: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "nothing to do") {
		t.Fatalf("output:\n%s", out.String())
	}
	if got := remoteBranchSHA(t, tgt, DefaultProposalBranch); got != tipB {
		t.Fatalf("idempotent re-run moved branch: %s → %s", tipB, got)
	}

	// Source advances again: the same branch is refreshed (§40, §45).
	writeFiles(t, src, map[string]string{"Makefile": "build:\n\tgo build ./...\n\nlint:\n\tgolangci-lint run\n"})
	shaC := commitAll(t, src, "C")
	out.Reset()
	if err := Propose(tgt, &out, ""); err != nil {
		t.Fatalf("refresh propose: %v\n%s", err, out.String())
	}
	tipC := remoteBranchSHA(t, tgt, DefaultProposalBranch)
	if tipC == tipB {
		t.Fatal("refresh did not move the proposal branch")
	}
	if got := runGit(t, origin, "show", "refs/heads/"+DefaultProposalBranch+":.git-scaffold/lock"); got != shaC {
		t.Fatalf("refreshed lock = %q, want %q", got, shaC)
	}
	if got := runGit(t, origin, "show", "refs/heads/"+DefaultProposalBranch+":Makefile"); !strings.Contains(got, "lint") {
		t.Fatalf("refreshed Makefile = %q", got)
	}
	// Still exactly one proposal branch beside main.
	heads := runGit(t, origin, "for-each-ref", "--format=%(refname)", "refs/heads")
	if heads != "refs/heads/git-scaffold/update\nrefs/heads/main" {
		t.Fatalf("origin branches:\n%s", heads)
	}
}

func TestProposeDirtyWorktreeUntouched(t *testing.T) {
	setEnv(t)
	src, shaA, tgt, _ := proposeTarget(t, "")
	advanceToB(t, src)
	// Unrelated local work: a hand-edited managed file and an untracked file.
	// Propose never touches the working tree, so neither blocks it (§49).
	writeFiles(t, tgt, map[string]string{
		"Makefile":  "hand edited\n",
		"local.txt": "scratch\n",
	})
	before := snapshot(t, tgt)
	var out bytes.Buffer
	if err := Propose(tgt, &out, ""); err != nil {
		t.Fatalf("propose: %v\n%s", err, out.String())
	}
	if remoteBranchSHA(t, tgt, DefaultProposalBranch) == "" {
		t.Fatal("proposal branch not pushed")
	}
	sameSnapshot(t, before, snapshot(t, tgt))
	if got := readFile(t, tgt, ".git-scaffold/lock"); got != shaA+"\n" {
		t.Fatalf("user lock advanced: %q", got)
	}
}

func TestProposeNoOriginRemote(t *testing.T) {
	setEnv(t)
	src, _, tgt := appliedTarget(t)
	commitAll(t, tgt, "base")
	advanceToB(t, src)
	err := Propose(tgt, io.Discard, "")
	if err == nil || !strings.Contains(err.Error(), "origin") {
		t.Fatalf("err = %v", err)
	}
}

// TestProposeGithubWithoutGh: a github.com origin whose transport is
// rewritten to a local bare repository via insteadOf. With gh absent the
// provider is treated as unknown: push succeeds, a notice is printed, exit 0.
func TestProposeGithubWithoutGh(t *testing.T) {
	setEnv(t)
	src, _, tgt, origin := proposeTarget(t, "")
	const ghURL = "https://github.com/acme/orders.git"
	runGit(t, tgt, "remote", "set-url", "origin", ghURL)
	runGit(t, tgt, "config", "url."+filepath.ToSlash(origin)+".insteadOf", ghURL)
	stubLookPath(t, map[string]string{})
	advanceToB(t, src)

	var out bytes.Buffer
	if err := Propose(tgt, &out, ""); err != nil {
		t.Fatalf("propose: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "automatic PR creation is unavailable") {
		t.Fatalf("output:\n%s", out.String())
	}
	if remoteBranchSHA(t, tgt, DefaultProposalBranch) == "" {
		t.Fatal("proposal branch not pushed")
	}
}

// TestProposeNonGithubOrigin: a bitbucket.org origin (transport rewritten to
// a local bare repository via insteadOf) is an unknown hosting provider
// (§43): the branch is pushed and the PR-unavailable notice is printed.
func TestProposeNonGithubOrigin(t *testing.T) {
	setEnv(t)
	src, _, tgt, origin := proposeTarget(t, "")
	const bbURL = "https://bitbucket.org/acme/orders.git"
	runGit(t, tgt, "remote", "set-url", "origin", bbURL)
	runGit(t, tgt, "config", "url."+filepath.ToSlash(origin)+".insteadOf", bbURL)
	advanceToB(t, src)

	var out bytes.Buffer
	if err := Propose(tgt, &out, ""); err != nil {
		t.Fatalf("propose: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "automatic PR creation is unavailable") {
		t.Fatalf("output:\n%s", out.String())
	}
	if remoteBranchSHA(t, tgt, DefaultProposalBranch) == "" {
		t.Fatal("proposal branch not pushed")
	}
}

// TestProposeGithubWithFakeGh drives the gh integration against a recorder
// script: `gh pr list` reports no open PR, so `gh pr create` must follow.
func TestProposeGithubWithFakeGh(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("recorder script requires /bin/sh")
	}
	setEnv(t)
	src, _, tgt, origin := proposeTarget(t, "")
	const ghURL = "https://github.com/acme/orders.git"
	runGit(t, tgt, "remote", "set-url", "origin", ghURL)
	runGit(t, tgt, "config", "url."+filepath.ToSlash(origin)+".insteadOf", ghURL)

	record := filepath.Join(t.TempDir(), "record")
	gh := writeScript(t, "gh", "#!/bin/sh\n"+
		"printf '%s\\n' \"$*\" >> "+record+"\n"+
		"if [ \"$2\" = list ]; then echo '[]'; fi\n")
	stubLookPath(t, map[string]string{"gh": gh})
	advanceToB(t, src)

	var out bytes.Buffer
	if err := Propose(tgt, &out, ""); err != nil {
		t.Fatalf("propose: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "opened PR") {
		t.Fatalf("output:\n%s", out.String())
	}
	rec := readFile(t, filepath.Dir(record), filepath.Base(record))
	lines := strings.Split(strings.TrimSpace(rec), "\n")
	if len(lines) != 2 {
		t.Fatalf("gh invocations:\n%s", rec)
	}
	if lines[0] != "pr list --head git-scaffold/update --state open --json number" {
		t.Fatalf("gh pr list argv = %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "pr create --head git-scaffold/update --title chore: update repository scaffold --body-file ") {
		t.Fatalf("gh pr create argv = %q", lines[1])
	}
}

// TestProposeCustomCreateCommand: a configured [propose] create-command
// replaces provider integration; placeholders are substituted per argv
// element and the command runs without a shell (§44).
func TestProposeCustomCreateCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("recorder script requires /bin/sh")
	}
	setEnv(t)
	record := filepath.Join(t.TempDir(), "record")
	bodyCopy := filepath.Join(t.TempDir(), "body.md")
	script := writeScript(t, "forge", "#!/bin/sh\n"+
		"printf '%s\\n' \"$@\" > "+record+"\n"+
		"cp \"$6\" "+bodyCopy+"\n")
	extra := "\n[propose]\ncreate-command = [\n" +
		"  \"" + filepath.ToSlash(script) + "\",\n" +
		"  \"--head\", \"{{ branch }}\",\n" +
		"  \"--title\", \"{{ title }}\",\n" +
		"  \"--body-file\", \"{{ body_file }}\",\n]\n"
	src, shaA, tgt, _ := proposeTarget(t, extra)
	shaB := advanceToB(t, src)

	var out bytes.Buffer
	if err := Propose(tgt, &out, "scaffold/refresh"); err != nil {
		t.Fatalf("propose: %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "automatic PR creation is unavailable") {
		t.Fatalf("provider fallback ran despite create-command:\n%s", out.String())
	}
	if remoteBranchSHA(t, tgt, "scaffold/refresh") == "" {
		t.Fatal("proposal branch not pushed under --branch override")
	}
	args := strings.Split(strings.TrimRight(readFile(t, filepath.Dir(record), filepath.Base(record)), "\n"), "\n")
	if len(args) != 6 || args[0] != "--head" || args[1] != "scaffold/refresh" ||
		args[2] != "--title" || args[3] != "chore: update repository scaffold" ||
		args[4] != "--body-file" || args[5] == "" {
		t.Fatalf("create-command argv = %q", args)
	}
	body := readFile(t, filepath.Dir(bodyCopy), filepath.Base(bodyCopy))
	for _, want := range []string{
		"Old: " + shaA, "New: " + shaB, "Ref: main",
		"M Makefile", "A .github/workflows/security.yml", "D .github/workflows/test.yml",
		"patches/golangci.json",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("proposal body missing %q:\n%s", want, body)
		}
	}
}

// stubLookPath replaces the gh lookup seam: names present in m resolve to
// their path, anything else is reported missing.
func stubLookPath(t *testing.T, m map[string]string) {
	t.Helper()
	orig := lookPath
	lookPath = func(name string) (string, error) {
		if p, ok := m[name]; ok {
			return p, nil
		}
		return "", exec.ErrNotFound
	}
	t.Cleanup(func() { lookPath = orig })
}

func writeScript(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestProviderHost covers §43 provider detection across the URL syntaxes git
// accepts: scheme URLs (with users, ports), scp-like syntax, and the local
// paths (including Windows drive letters) that must yield no provider.
func TestProviderHost(t *testing.T) {
	for url, want := range map[string]string{
		"git@github.com:o/r.git":                    "github.com",
		"github.com:o/r.git":                        "github.com",
		"ssh://git@github.com/o/r.git":              "github.com",
		"ssh://git@GitHub.com:22/o/r.git":           "github.com",
		"https://github.com/o/r.git":                "github.com",
		"https://user:pass@github.com:8443/o/r.git": "github.com",
		"https://gitlab.example.com/o/r.git":        "gitlab.example.com",
		"/local/path/repo.git":                      "",
		"C:/repos/x.git":                            "",
		`C:\repos\x.git`:                            "",
		"file:///tmp/x.git":                         "",
		"":                                          "",
	} {
		if got := providerHost(url); got != want {
			t.Errorf("providerHost(%q) = %q, want %q", url, got, want)
		}
	}
}

// TestProposeGhExistingPR: when `gh pr list` reports an open PR for the
// branch, propose refreshes it and must not run `gh pr create` (§43, §45).
func TestProposeGhExistingPR(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("recorder script requires /bin/sh")
	}
	setEnv(t)
	src, _, tgt, origin := proposeTarget(t, "")
	const ghURL = "https://github.com/acme/orders.git"
	runGit(t, tgt, "remote", "set-url", "origin", ghURL)
	runGit(t, tgt, "config", "url."+filepath.ToSlash(origin)+".insteadOf", ghURL)

	record := filepath.Join(t.TempDir(), "record")
	gh := writeScript(t, "gh", "#!/bin/sh\n"+
		"printf '%s\\n' \"$*\" >> "+record+"\n"+
		"if [ \"$2\" = list ]; then echo '[{\"number\":7}]'; fi\n")
	stubLookPath(t, map[string]string{"gh": gh})
	advanceToB(t, src)

	var out bytes.Buffer
	if err := Propose(tgt, &out, ""); err != nil {
		t.Fatalf("propose: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "existing open PR #7 refreshed") {
		t.Fatalf("output:\n%s", out.String())
	}
	if strings.Contains(out.String(), "opened PR") {
		t.Fatalf("created a PR despite one existing:\n%s", out.String())
	}
	lines := strings.Split(strings.TrimSpace(readFile(t, filepath.Dir(record), filepath.Base(record))), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "pr list ") {
		t.Fatalf("gh invocations:\n%s", strings.Join(lines, "\n"))
	}
}

// TestProposeCreateCommandFailure: a create-command exiting non-zero fails
// the propose, carrying the tool's stderr (§44).
func TestProposeCreateCommandFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("recorder script requires /bin/sh")
	}
	setEnv(t)
	script := writeScript(t, "forge", "#!/bin/sh\necho 'forge: no credentials' >&2\nexit 3\n")
	extra := "\n[propose]\ncreate-command = [\"" + filepath.ToSlash(script) + "\", \"{{ branch }}\"]\n"
	src, _, tgt, _ := proposeTarget(t, extra)
	advanceToB(t, src)

	var out bytes.Buffer
	err := Propose(tgt, &out, "")
	if err == nil {
		t.Fatalf("propose succeeded despite failing create-command:\n%s", out.String())
	}
	for _, want := range []string{"propose create-command", "no credentials"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, missing %q", err, want)
		}
	}
	if strings.Contains(out.String(), "proposed scaffold update") {
		t.Fatalf("success line printed despite failure:\n%s", out.String())
	}
}

// TestProposalBaseLocalHeadFallback: an origin with no commits advertises no
// HEAD, so the proposal builds on the local HEAD instead.
func TestProposalBaseLocalHeadFallback(t *testing.T) {
	setEnv(t)
	repo := newRepo(t)
	writeFiles(t, repo, map[string]string{"a.txt": "A\n"})
	sha := commitAll(t, repo, "A")
	originDir := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, repo, "init", "-q", "--bare", "-b", "main", originDir)
	runGit(t, repo, "remote", "add", "origin", originDir)

	got, err := proposalBase(repo)
	if err != nil {
		t.Fatalf("proposalBase: %v", err)
	}
	if got != sha {
		t.Fatalf("proposalBase = %s, want local HEAD %s", got, sha)
	}
}
