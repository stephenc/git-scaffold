package updatecheck

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// setup isolates the check from the developer's real environment: fresh
// config and state homes, no disable switches, a TTY.
func setup(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("GIT_SCAFFOLD_NO_UPDATE_CHECK", "")
	t.Setenv("CI", "")
	orig := IsTTY
	IsTTY = func() bool { return true }
	t.Cleanup(func() { IsTTY = orig })
}

// serve points BaseURL at a server that redirects /releases/latest to the
// given tag, GitHub-style, and restores BaseURL afterwards.
func serve(t *testing.T, tag string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/releases/latest" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Location", "/releases/tag/"+tag)
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	orig := BaseURL
	BaseURL = srv.URL
	t.Cleanup(func() { BaseURL = orig })
}

// notice runs a full Start/wait cycle and returns what was printed.
func notice(t *testing.T, current string) string {
	t.Helper()
	var sb strings.Builder
	done := make(chan struct{})
	wait := Start(current)
	go func() { wait(&sb); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("update check did not finish")
	}
	return sb.String()
}

func statefile(t *testing.T) string {
	t.Helper()
	return filepath.Join(os.Getenv("XDG_STATE_HOME"), "git-scaffold", "update-check")
}

func TestNewerVersionPrompts(t *testing.T) {
	setup(t)
	serve(t, "v2.0.1")
	got := notice(t, "1.9.9")
	if !strings.Contains(got, "2.0.1") || !strings.Contains(got, "1.9.9") ||
		!strings.Contains(got, "/releases/latest") {
		t.Fatalf("notice = %q", got)
	}
	if strings.Count(strings.TrimRight(got, "\n"), "\n") != 0 {
		t.Fatalf("notice is not one line: %q", got)
	}
	// The check was recorded: mtime gates, content is the last-seen tag.
	data, err := os.ReadFile(statefile(t))
	if err != nil || strings.TrimSpace(string(data)) != "v2.0.1" {
		t.Fatalf("state = %q, %v", data, err)
	}
}

func TestOlderEqualGarbageTagSilent(t *testing.T) {
	for _, tag := range []string{"v1.0.0", "v1.2.3", "v1.2.3-rc.1", "not-a-version", ""} {
		t.Run("tag="+tag, func(t *testing.T) {
			setup(t)
			serve(t, tag)
			if got := notice(t, "1.2.3"); got != "" {
				t.Fatalf("notice = %q, want none", got)
			}
		})
	}
}

func TestIntervalGating(t *testing.T) {
	setup(t)
	serve(t, "v9.0.0")
	state := statefile(t)
	if err := os.MkdirAll(filepath.Dir(state), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state, []byte("v9.0.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A recent check (fresh mtime) suppresses the next one entirely.
	if got := notice(t, "1.0.0"); got != "" {
		t.Fatalf("within interval: notice = %q", got)
	}

	// A stale mtime lets the check run again.
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(state, old, old); err != nil {
		t.Fatal(err)
	}
	if got := notice(t, "1.0.0"); got == "" {
		t.Fatal("past interval: expected a notice")
	}

	// A shorter configured interval is honored.
	if err := os.WriteFile(filepath.Join(mkConfigDir(t), "config.toml"),
		[]byte("[update-check]\ninterval = \"1ms\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if got := notice(t, "1.0.0"); got == "" {
		t.Fatal("1ms interval: expected a notice")
	}
}

func mkConfigDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "git-scaffold")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCorruptStateStillWorks(t *testing.T) {
	setup(t)
	serve(t, "v2.0.0")
	state := statefile(t)
	if err := os.MkdirAll(filepath.Dir(state), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state, []byte("\x00garbage\xff not a tag"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(state, old, old); err != nil {
		t.Fatal(err)
	}
	if got := notice(t, "1.0.0"); got == "" {
		t.Fatal("corrupt state: expected a notice")
	}
}

func TestDisableSwitches(t *testing.T) {
	cases := map[string]func(t *testing.T){
		"env": func(t *testing.T) { t.Setenv("GIT_SCAFFOLD_NO_UPDATE_CHECK", "1") },
		"ci":  func(t *testing.T) { t.Setenv("CI", "true") },
		"config": func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(mkConfigDir(t), "config.toml"),
				[]byte("[update-check]\nenabled = false\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"non-tty": func(t *testing.T) { IsTTY = func() bool { return false } },
	}
	for name, disable := range cases {
		t.Run(name, func(t *testing.T) {
			setup(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Error("disabled check must not touch the network")
			}))
			t.Cleanup(srv.Close)
			orig := BaseURL
			BaseURL = srv.URL
			t.Cleanup(func() { BaseURL = orig })
			disable(t)
			if got := notice(t, "1.0.0"); got != "" {
				t.Fatalf("notice = %q, want none", got)
			}
		})
	}
}

func TestNonSemverCurrentNeverPrompts(t *testing.T) {
	for _, v := range []string{"abc1234def56", "abc1234def56-dirty", "unknown", "(devel)", "1.2.3-rc.1"} {
		t.Run(v, func(t *testing.T) {
			setup(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Error("non-semver build must not touch the network")
			}))
			t.Cleanup(srv.Close)
			orig := BaseURL
			BaseURL = srv.URL
			t.Cleanup(func() { BaseURL = orig })
			if got := notice(t, v); got != "" {
				t.Fatalf("notice = %q, want none", got)
			}
		})
	}
}

func TestServerFailureSilent(t *testing.T) {
	// Connection refused: a server that is already closed.
	setup(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	orig := BaseURL
	BaseURL = url
	t.Cleanup(func() { BaseURL = orig })
	if got := notice(t, "1.0.0"); got != "" {
		t.Fatalf("refused connection: notice = %q", got)
	}
}

func TestServerTimeoutSilent(t *testing.T) {
	setup(t)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // hang past the client timeout
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	orig := BaseURL
	BaseURL = srv.URL
	t.Cleanup(func() { BaseURL = orig })
	start := time.Now()
	if got := notice(t, "1.0.0"); got != "" {
		t.Fatalf("timeout: notice = %q", got)
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Fatalf("wait took %v, must be bounded by the timeout", d)
	}
}

// TestFailedCheckStillRecordsState: the interval gates attempts, not
// successes — a failing endpoint must not be re-attempted on every command.
func TestFailedCheckStillRecordsState(t *testing.T) {
	setup(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	orig := BaseURL
	BaseURL = srv.URL
	t.Cleanup(func() { BaseURL = orig })

	if got := notice(t, "1.0.0"); got != "" {
		t.Fatalf("failed check printed %q", got)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("hits = %d, want 1", n)
	}
	// The attempt was recorded despite the failure.
	if _, err := os.Stat(statefile(t)); err != nil {
		t.Fatalf("state not recorded after failed check: %v", err)
	}

	// A second command within the interval must not hit the server at all.
	if got := notice(t, "1.0.0"); got != "" {
		t.Fatalf("second check printed %q", got)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("second command within interval hit the server: hits = %d", n)
	}
}

func TestNon3xxSilent(t *testing.T) {
	setup(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	orig := BaseURL
	BaseURL = srv.URL
	t.Cleanup(func() { BaseURL = orig })
	if got := notice(t, "1.0.0"); got != "" {
		t.Fatalf("404: notice = %q", got)
	}
}

func TestParseSemver(t *testing.T) {
	good := map[string][3]int{
		"1.2.3": {1, 2, 3}, "v1.2.3": {1, 2, 3}, "0.0.0": {0, 0, 0}, "10.20.30": {10, 20, 30},
	}
	for s, want := range good {
		v, ok := parseSemver(s)
		if !ok || v != (semver{want[0], want[1], want[2]}) {
			t.Errorf("parseSemver(%q) = %v, %v", s, v, ok)
		}
	}
	for _, s := range []string{"", "1.2", "1.2.3.4", "1.2.x", "1.2.3-rc.1", "abc1234", "unknown", "1..3"} {
		if _, ok := parseSemver(s); ok {
			t.Errorf("parseSemver(%q) accepted", s)
		}
	}
	if !(semver{1, 2, 3}).less(semver{1, 2, 4}) || !(semver{1, 9, 9}).less(semver{2, 0, 0}) ||
		(semver{1, 2, 3}).less(semver{1, 2, 3}) || (semver{2, 0, 0}).less(semver{1, 9, 9}) {
		t.Error("semver ordering wrong")
	}
}

// TestStatePathHomeFallback: with XDG_STATE_HOME unset, the state file
// lives under ~/.local/state.
func TestStatePathHomeFallback(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)        // unix
	t.Setenv("USERPROFILE", home) // windows
	got, err := statePath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".local", "state", "git-scaffold", "update-check"); got != want {
		t.Fatalf("statePath() = %q, want %q", got, want)
	}
}

// TestWriteStateFailureSilent: an unwritable state location (a regular file
// where the state directory should be) is a silent no-op, never an error or
// a panic — the next run simply retries.
func TestWriteStateFailureSilent(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(blocker, "git-scaffold", "update-check")
	writeState(path, "v1.2.3")
	if _, err := os.Stat(path); err == nil {
		t.Fatal("state file created through a blocking regular file")
	}
	// The blocker itself is untouched.
	data, err := os.ReadFile(blocker)
	if err != nil || len(data) != 0 {
		t.Fatalf("blocker disturbed: %q, %v", data, err)
	}
}
