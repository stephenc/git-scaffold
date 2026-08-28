package materialize

import (
	"errors"
	"testing"

	"github.com/stephenc/git-scaffold/internal/config"
)

// The override stage reports failures as *OverrideError so callers can
// tell broken overrides apart from a scaffold that cannot materialize.
func TestOverrideStageErrorsAreTyped(t *testing.T) {
	d, err := config.ParseDescriptor([]byte("[scaffold]\nversion = 1\n[[files]]\npath = \"a.txt\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	src := map[string][]byte{"a.txt": []byte("hello\n")}
	cases := map[string]*config.TargetConfig{
		"unmanaged": {Overrides: map[string]config.Override{"b.txt": {Strategy: config.StrategyTextPatch, Patches: []string{"p"}}}},
		"forbidden": {Overrides: map[string]config.Override{"a.txt": {Strategy: config.StrategyJSONPatch, Patches: []string{"p"}}}},
		"missing":   {Overrides: map[string]config.Override{"a.txt": {Strategy: config.StrategyTextPatch, Patches: []string{"nope"}}}},
		"broken":    {Overrides: map[string]config.Override{"a.txt": {Strategy: config.StrategyTextPatch, Patches: []string{"p"}}}},
	}
	patches := map[string][]byte{"p": []byte("--- a\n+++ b\n@@ -1 +1 @@\n-nomatch\n+x\n")}
	for name, cfg := range cases {
		_, err := Materialize(src, d, cfg, patches)
		var oe *OverrideError
		if !errors.As(err, &oe) {
			t.Errorf("%s: want *OverrideError, got %T: %v", name, err, err)
		}
	}
	// Failures before the override stage are not OverrideErrors.
	_, err = Materialize(src, d, &config.TargetConfig{Args: map[string]string{"x": "y"}}, nil)
	var oe *OverrideError
	if err == nil || errors.As(err, &oe) {
		t.Errorf("pre-override failure must not be an OverrideError: %v", err)
	}
}
