// Package globalcfg parses the optional per-user tool configuration in
// $XDG_CONFIG_HOME/git-scaffold/config.toml (falling back to
// ~/.config/git-scaffold/config.toml — plain XDG on every platform, matching
// git's own XDG handling). The file configures the tool, never a scaffold:
// the cache directory and the self update-check (§56). An absent file yields
// the defaults; unknown keys are rejected so typos surface instead of being
// silently ignored, in the same spirit as the target-config parsing.
package globalcfg

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// DefaultInterval is the default update-check interval (§56).
const DefaultInterval = 24 * time.Hour

// UpdateCheck is the [update-check] table.
type UpdateCheck struct {
	Enabled  bool
	Interval time.Duration
}

// Config is the parsed global configuration with defaults applied.
type Config struct {
	// CacheDir overrides the built-in cache location when non-empty (§56).
	// The env override GIT_SCAFFOLD_CACHE is applied by CacheDir, not here.
	CacheDir    string
	UpdateCheck UpdateCheck
}

type rawUpdateCheck struct {
	Enabled  *bool   `toml:"enabled"`
	Interval *string `toml:"interval"`
}

type raw struct {
	CacheDir    *string         `toml:"cache-dir"`
	UpdateCheck *rawUpdateCheck `toml:"update-check"`
}

// Path returns the global configuration file path, whether or not it exists.
func Path() (string, error) {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "git-scaffold", "config.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine config directory: %w", err)
	}
	return filepath.Join(home, ".config", "git-scaffold", "config.toml"), nil
}

// Load reads and parses the global configuration. An absent file is not an
// error: zero configuration keeps working, with all defaults in effect.
func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Parse(nil)
	}
	if err != nil {
		return nil, err
	}
	cfg, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Parse parses global configuration content with defaults applied.
func Parse(data []byte) (*Config, error) {
	cfg := &Config{UpdateCheck: UpdateCheck{Enabled: true, Interval: DefaultInterval}}
	var r raw
	md, err := toml.Decode(string(data), &r)
	if err != nil {
		return nil, fmt.Errorf("invalid TOML: %w", err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf("unknown key %q", undecoded[0].String())
	}
	if r.CacheDir != nil {
		dir, err := expandHome(*r.CacheDir)
		if err != nil {
			return nil, fmt.Errorf("cache-dir: %w", err)
		}
		if dir == "" {
			return nil, fmt.Errorf("cache-dir: must not be empty")
		}
		cfg.CacheDir = dir
	}
	if r.UpdateCheck != nil {
		if r.UpdateCheck.Enabled != nil {
			cfg.UpdateCheck.Enabled = *r.UpdateCheck.Enabled
		}
		if r.UpdateCheck.Interval != nil {
			d, err := time.ParseDuration(*r.UpdateCheck.Interval)
			if err != nil {
				return nil, fmt.Errorf("update-check.interval: invalid duration %q", *r.UpdateCheck.Interval)
			}
			if d <= 0 {
				return nil, fmt.Errorf("update-check.interval: must be positive, got %q", *r.UpdateCheck.Interval)
			}
			cfg.UpdateCheck.Interval = d
		}
	}
	return cfg, nil
}

// expandHome expands a leading "~" or "~/" to the user's home directory.
func expandHome(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot expand %q: %w", p, err)
	}
	if p == "~" {
		return home, nil
	}
	return filepath.Join(home, filepath.FromSlash(p[2:])), nil
}

// CacheDir resolves the source cache directory with the full precedence of
// §56: $GIT_SCAFFOLD_CACHE, else the global config cache-dir, else
// $XDG_CACHE_HOME/git-scaffold, else ~/.cache/git-scaffold. The env override
// is checked before the config file is read so that it always wins, even
// over an unreadable config.
func CacheDir() (string, error) {
	if d := os.Getenv("GIT_SCAFFOLD_CACHE"); d != "" {
		return d, nil
	}
	cfg, err := Load()
	if err != nil {
		return "", err
	}
	if cfg.CacheDir != "" {
		return cfg.CacheDir, nil
	}
	if d := os.Getenv("XDG_CACHE_HOME"); d != "" {
		return filepath.Join(d, "git-scaffold"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine cache directory: %w", err)
	}
	return filepath.Join(home, ".cache", "git-scaffold"), nil
}
