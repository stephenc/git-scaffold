// Package updatecheck implements the opt-out self update-check (§56): at
// most once per configured interval, and only when stderr is a terminal, it
// asks GitHub for the latest release tag by reading the redirect Location of
// .../releases/latest — no API client, no JSON — and prints a single notice
// line to stderr when a newer semver release exists. Every failure mode
// (network, timeout, unreadable state, unparsable tags) is a silent no-op:
// the check must never delay a command beyond its timeout, never change an
// exit code, and never write to stdout.
package updatecheck

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/stephenc/git-scaffold/internal/globalcfg"
)

// BaseURL is the release host. It is a variable so tests can point the
// check at an httptest server.
var BaseURL = "https://github.com/stephenc/git-scaffold"

// timeout bounds the total wait for the check's result (§56: the check
// must never delay a command by more than 2 seconds).
const timeout = 2 * time.Second

// httpTimeout bounds the HTTP request; it sits below timeout so a
// timed-out request still reports back within the overall wait bound.
const httpTimeout = 1800 * time.Millisecond

// IsTTY reports whether stderr is a terminal. It is a variable so tests can
// force either answer.
var IsTTY = func() bool {
	fi, err := os.Stderr.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// Start decides whether a check is due and, if so, launches it in the
// background. The returned function waits (at most the timeout) for the
// result and prints the one-line notice, if any, to w; when no check runs
// it is a no-op. Callers invoke it once, after command output.
func Start(current string) func(io.Writer) {
	noop := func(io.Writer) {}
	if os.Getenv("GIT_SCAFFOLD_NO_UPDATE_CHECK") != "" || os.Getenv("CI") != "" {
		return noop
	}
	if !IsTTY() {
		return noop
	}
	cur, ok := parseSemver(current)
	if !ok {
		// Dev builds report a commit hash or "unknown"; never prompt.
		return noop
	}
	cfg, err := globalcfg.Load()
	if err != nil || !cfg.UpdateCheck.Enabled {
		return noop
	}
	state, err := statePath()
	if err != nil {
		return noop
	}
	if fi, err := os.Stat(state); err == nil && time.Since(fi.ModTime()) < cfg.UpdateCheck.Interval {
		return noop
	}
	ch := make(chan string, 1)
	go func() { ch <- check(current, cur, state) }()
	return func(w io.Writer) {
		select {
		case msg := <-ch:
			if msg != "" {
				io.WriteString(w, msg)
			}
		case <-time.After(timeout):
		}
	}
}

// check fetches the latest release tag, records the check in the state
// file, and returns the notice line, or "" for a silent no-op.
func check(currentStr string, cur semver, state string) string {
	// Record the attempt up front so the interval gates attempts, not
	// successes: a persistently failing network must not re-attempt on
	// every command.
	touchState(state)
	client := &http.Client{
		Timeout: httpTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(BaseURL + "/releases/latest")
	if err != nil {
		return ""
	}
	resp.Body.Close()
	if resp.StatusCode < 300 || resp.StatusCode > 399 {
		return ""
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		return ""
	}
	tag := loc.Path[strings.LastIndex(loc.Path, "/")+1:]
	writeState(state, tag)
	latest, ok := parseSemver(strings.TrimPrefix(tag, "v"))
	if !ok || !cur.less(latest) {
		return ""
	}
	return fmt.Sprintf(
		"\033[1m✨ git-scaffold %s is available (you have %s) — %s/releases/latest\033[0m\n",
		strings.TrimPrefix(tag, "v"), currentStr, BaseURL)
}

// statePath returns the update-check state file: the last-check timestamp
// is its mtime, the last-seen tag its content.
func statePath() (string, error) {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "git-scaffold", "update-check"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "git-scaffold", "update-check"), nil
}

// touchState bumps the state file's mtime (creating an empty file if
// needed) without disturbing the last-seen-tag content, so a failed check
// still counts against the interval. Failure is a silent no-op.
func touchState(path string) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	now := time.Now()
	if err := os.Chtimes(path, now, now); err != nil {
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			f.Close()
		}
	}
}

func writeState(path, tag string) {
	// Failure to record state is a silent no-op: the next run retries.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, []byte(tag+"\n"), 0o644)
}

type semver struct{ major, minor, patch int }

// parseSemver accepts plain X.Y.Z (an optional leading "v" tolerated).
// Anything else — commit hashes, "unknown", pre-releases — is not a
// comparable release version.
func parseSemver(s string) (semver, bool) {
	s = strings.TrimPrefix(s, "v")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	nums := [3]int{}
	for i, p := range parts {
		if p == "" || strings.TrimLeft(p, "0123456789") != "" {
			return semver{}, false
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return semver{}, false
		}
		nums[i] = n
	}
	return semver{nums[0], nums[1], nums[2]}, true
}

func (a semver) less(b semver) bool {
	if a.major != b.major {
		return a.major < b.major
	}
	if a.minor != b.minor {
		return a.minor < b.minor
	}
	return a.patch < b.patch
}
