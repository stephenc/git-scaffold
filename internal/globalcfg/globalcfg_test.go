package globalcfg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeConfig points $XDG_CONFIG_HOME at a fresh directory and writes the
// global config file there; content == "" writes no file at all.
func writeConfig(t *testing.T, content string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if content == "" {
		return
	}
	path := filepath.Join(dir, "git-scaffold", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAbsentFile(t *testing.T) {
	writeConfig(t, "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.CacheDir != "" || !cfg.UpdateCheck.Enabled || cfg.UpdateCheck.Interval != DefaultInterval {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
}

func TestLoadEmptyFile(t *testing.T) {
	writeConfig(t, "# nothing configured\n")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.CacheDir != "" || !cfg.UpdateCheck.Enabled || cfg.UpdateCheck.Interval != DefaultInterval {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
}

func TestLoadFull(t *testing.T) {
	writeConfig(t, "cache-dir = \"/var/cache/gs\"\n\n[update-check]\nenabled = false\ninterval = \"1h30m\"\n")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.CacheDir != "/var/cache/gs" {
		t.Fatalf("CacheDir = %q", cfg.CacheDir)
	}
	if cfg.UpdateCheck.Enabled {
		t.Fatal("Enabled should be false")
	}
	if cfg.UpdateCheck.Interval != 90*time.Minute {
		t.Fatalf("Interval = %v", cfg.UpdateCheck.Interval)
	}
}

func TestUnknownKeysRejected(t *testing.T) {
	for _, content := range []string{
		"cache_dir = \"/x\"\n", // typo: underscore for hyphen
		"[update-check]\nintervall = \"24h\"\n",
		"[updatecheck]\nenabled = false\n",
	} {
		writeConfig(t, content)
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), "unknown key") {
			t.Errorf("Load(%q) err = %v, want unknown key error", content, err)
		}
	}
}

func TestBadDuration(t *testing.T) {
	for _, iv := range []string{"daily", "-1h", "0s"} {
		writeConfig(t, "[update-check]\ninterval = \""+iv+"\"\n")
		if _, err := Load(); err == nil {
			t.Errorf("interval %q: expected error", iv)
		}
	}
}

func TestTildeExpansion(t *testing.T) {
	writeConfig(t, "cache-dir = \"~/my/cache\"\n")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "my", "cache"); cfg.CacheDir != want {
		t.Fatalf("CacheDir = %q, want %q", cfg.CacheDir, want)
	}
}

func TestCacheDirPrecedence(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/xdg-cache")

	// XDG default when neither env nor config configures a directory.
	writeConfig(t, "")
	t.Setenv("GIT_SCAFFOLD_CACHE", "")
	dir, err := CacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/xdg-cache", "git-scaffold"); dir != want {
		t.Fatalf("default: %q, want %q", dir, want)
	}

	// Global config beats the XDG default.
	writeConfig(t, "cache-dir = \"/from-config\"\n")
	dir, err = CacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != "/from-config" {
		t.Fatalf("config: %q", dir)
	}

	// The env override beats the config — and wins without even reading it,
	// so a broken config cannot break env-pinned runs (tests rely on this).
	writeConfig(t, "not even valid toml [\n")
	t.Setenv("GIT_SCAFFOLD_CACHE", "/from-env")
	dir, err = CacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != "/from-env" {
		t.Fatalf("env: %q", dir)
	}

	// Without the env override, a broken config is a loud error, not a
	// silently ignored typo.
	t.Setenv("GIT_SCAFFOLD_CACHE", "")
	if _, err := CacheDir(); err == nil {
		t.Fatal("broken config: expected error")
	}
}

// setTempHome makes os.UserHomeDir resolve to a fresh directory on every
// platform, so the XDG fallbacks are exercised without touching the real
// home directory.
func setTempHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)        // unix
	t.Setenv("USERPROFILE", home) // windows
	return home
}

// TestPathHomeFallback: with XDG_CONFIG_HOME unset, the config lives under
// ~/.config (plain XDG on every platform, matching git).
func TestPathHomeFallback(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	home := setTempHome(t)
	got, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".config", "git-scaffold", "config.toml"); got != want {
		t.Fatalf("Path() = %q, want %q", got, want)
	}
}

// TestCacheDirHomeFallback: with no env override, no config file, and no
// XDG_CACHE_HOME, the cache falls back to ~/.cache/git-scaffold.
func TestCacheDirHomeFallback(t *testing.T) {
	t.Setenv("GIT_SCAFFOLD_CACHE", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	home := setTempHome(t)
	got, err := CacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".cache", "git-scaffold"); got != want {
		t.Fatalf("CacheDir() = %q, want %q", got, want)
	}
}

// TestCacheDirFromConfigWithoutXDG: the config file's cache-dir wins when no
// env override is set, even without XDG_CACHE_HOME in the environment.
func TestCacheDirFromConfigWithoutXDG(t *testing.T) {
	t.Setenv("GIT_SCAFFOLD_CACHE", "")
	t.Setenv("XDG_CACHE_HOME", "")
	dir := filepath.Join(t.TempDir(), "chosen-cache")
	writeConfig(t, "cache-dir = \""+filepath.ToSlash(dir)+"\"\n")
	got, err := CacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(got) != dir {
		t.Fatalf("CacheDir() = %q, want %q", got, dir)
	}
}
