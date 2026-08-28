package glob

import (
	"reflect"
	"testing"
)

func TestHasMeta(t *testing.T) {
	tests := []struct {
		pattern string
		want    bool
	}{
		{"Makefile", false},
		{".github/workflows/ci.yml", false},
		{"*.md", true},
		{"docs/?.md", true},
		{"examples/**/*", true},
	}
	for _, tt := range tests {
		if got := HasMeta(tt.pattern); got != tt.want {
			t.Errorf("HasMeta(%q) = %v, want %v", tt.pattern, got, tt.want)
		}
	}
}

func TestMatch(t *testing.T) {
	tests := []struct {
		pattern, path string
		want          bool
	}{
		// Exact.
		{"Makefile", "Makefile", true},
		{"Makefile", "makefile", false},
		{"a/b", "a/b", true},

		// Root-level `*` does not cross separators.
		{"*.md", "README.md", true},
		{"*.md", "docs/README.md", false},
		{"*", "README.md", true},
		{"*", "docs/README.md", false},

		// `*` within a component.
		{".github/workflows/*.yml", ".github/workflows/ci.yml", true},
		{".github/workflows/*.yml", ".github/workflows/nested/ci.yml", false},
		{".github/workflows/*.yml", ".github/workflows/ci.yaml", false},
		{"a*b", "ab", true},
		{"a*b", "axxxb", true},
		{"a*b", "axxx", false},

		// `?` matches exactly one non-separator character.
		{"docs/?.md", "docs/a.md", true},
		{"docs/?.md", "docs/ab.md", false},
		{"docs/?.md", "docs/.md", false},
		{"?", "a", true},
		{"?", "a/b", false},
		{"?", "é", true}, // one character, not one byte

		// `**` matches zero or more complete path components.
		{"examples/**/*", "examples/a.txt", true},
		{"examples/**/*", "examples/x/y/a.txt", true},
		{"examples/**/*", "examples", false},
		{"**/*.yml", "ci.yml", true},
		{"**/*.yml", "a/b/ci.yml", true},
		{".github/ISSUE_TEMPLATE/**", ".github/ISSUE_TEMPLATE/bug.md", true},
		{".github/ISSUE_TEMPLATE/**", ".github/ISSUE_TEMPLATE/sub/x.md", true},
		{".github/ISSUE_TEMPLATE/**", ".github/other/x.md", false},
		{"config/**/*.yaml", "config/a.yaml", true},
		{"config/**/*.yaml", "config/prod/a.yaml", true},
		{"config/**/*.yaml", "config/prod/a.yml", false},

		// `**` inside a component is not recursive: each `*` matches within
		// the component.
		{"a**b", "ab", true},
		{"a**b", "axb", true},
		{"a**b", "a/b", false},
	}
	for _, tt := range tests {
		if got := Match(tt.pattern, tt.path); got != tt.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tt.pattern, tt.path, got, tt.want)
		}
	}
}

func TestExpandSortsLexicographically(t *testing.T) {
	// Input order must not affect output order (§9.4).
	paths := []string{"b/z.yml", "a/x.yml", "b/a.yml", "a/x.txt"}
	got := Expand("**/*.yml", paths)
	want := []string{"a/x.yml", "b/a.yml", "b/z.yml"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Expand = %v, want %v", got, want)
	}
}

func TestExpandNoMatches(t *testing.T) {
	if got := Expand("*.toml", []string{"a.yml"}); len(got) != 0 {
		t.Errorf("Expand = %v, want empty", got)
	}
}
