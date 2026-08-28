package cmd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stephenc/git-scaffold/internal/updatecheck"
)

// TestNoUpdateCheckTouchesNoNetwork verifies the wire-through: a command run
// with GIT_SCAFFOLD_NO_UPDATE_CHECK set never contacts the release host.
func TestNoUpdateCheckTouchesNoNetwork(t *testing.T) {
	t.Setenv("GIT_SCAFFOLD_NO_UPDATE_CHECK", "1")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("update check hit the network despite GIT_SCAFFOLD_NO_UPDATE_CHECK")
	}))
	t.Cleanup(srv.Close)
	origURL := updatecheck.BaseURL
	updatecheck.BaseURL = srv.URL
	t.Cleanup(func() { updatecheck.BaseURL = origURL })
	origTTY := updatecheck.IsTTY
	updatecheck.IsTTY = func() bool { return true }
	t.Cleanup(func() { updatecheck.IsTTY = origTTY })

	out, err := execute(t, "version")
	if err != nil {
		t.Fatalf("version: %v\n%s", err, out)
	}
	if !strings.Contains(out, "git-scaffold ") {
		t.Fatalf("version output: %q", out)
	}
	// Drain the (skipped) check the way Execute does; any pending request
	// would have to finish before the handler above could fail the test.
	updateNotice(io.Discard)
}
