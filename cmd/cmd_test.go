package cmd

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func setEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "no-global-config"))
	t.Setenv("GIT_AUTHOR_NAME", "test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
	t.Setenv("GIT_SCAFFOLD_CACHE", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("GIT_SCAFFOLD_NO_UPDATE_CHECK", "1")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func execute(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs(args)
	err := rootCmd.Execute()
	return out.String(), err
}

// TestCobraLifecycle drives init/check through cobra argument parsing.
func TestCobraLifecycle(t *testing.T) {
	setEnv(t)
	src := t.TempDir()
	runGit(t, src, "init", "-q", "-b", "main")
	files := map[string]string{
		".git-scaffold/config.toml": "[scaffold]\nversion = 1\n\n" +
			"[[arguments]]\nname = \"name\"\ntoken = \"@@NAME@@\"\n\n" +
			"[[files]]\npath = \"README.md\"\n",
		"README.md": "hello @@NAME@@\n",
	}
	for p, c := range files {
		abs := filepath.Join(src, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, src, "add", "-A")
	runGit(t, src, "commit", "-q", "-m", "A")

	tgt := t.TempDir()
	runGit(t, tgt, "init", "-q", "-b", "main")
	t.Chdir(tgt)

	if out, err := execute(t, "init", filepath.ToSlash(src), "--ref", "main", "--arg", "name=world"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	got, err := os.ReadFile(filepath.Join(tgt, "README.md"))
	if err != nil || string(got) != "hello world\n" {
		t.Fatalf("README.md = %q, %v", got, err)
	}

	if out, err := execute(t, "check"); err != nil {
		t.Fatalf("check: %v\n%s", err, out)
	}

	if err := os.WriteFile(filepath.Join(tgt, "README.md"), []byte("tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, "check")
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != 1 {
		t.Fatalf("check after edit: err = %v", err)
	}
	if !strings.Contains(out, "modified: README.md") {
		t.Fatalf("check output:\n%s", out)
	}

	// `outdated` maps its answer and errors to §38 exit statuses.
	if out, err := execute(t, "outdated"); err != nil {
		t.Fatalf("outdated: %v\n%s", err, out)
	}
}
