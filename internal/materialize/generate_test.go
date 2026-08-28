package materialize

import (
	"reflect"
	"strings"
	"testing"
)

// roundTrip generates a patch from base to want and verifies that applying
// it through the real applier reproduces want structurally.
func roundTrip(t *testing.T, path, base, want string) string {
	t.Helper()
	patch, err := GenerateJSONPatch(path, []byte(base), []byte(want))
	if err != nil {
		t.Fatalf("GenerateJSONPatch: %v", err)
	}
	if !strings.HasSuffix(string(patch), "]\n") || !strings.HasPrefix(string(patch), "[") {
		t.Fatalf("patch is not a JSON array with trailing newline:\n%s", patch)
	}
	got, err := ApplyJSONPatch(path, "patches/p", []byte(base), patch)
	if err != nil {
		t.Fatalf("apply generated patch:\n%s\n%v", patch, err)
	}
	g, err := DecodeStructured(path, got)
	if err != nil {
		t.Fatal(err)
	}
	w, err := DecodeStructured(path, []byte(want))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("round trip mismatch:\npatch:\n%s\ngot:\n%s", patch, got)
	}
	return string(patch)
}

func TestGenerateJSONPatchRoundTrips(t *testing.T) {
	cases := []struct {
		name, path, base, want string
		contains               []string
	}{
		{
			name: "nested object replace",
			path: "a.json",
			base: `{"run": {"timeout": "5m", "tests": true}}`,
			want: `{"run": {"timeout": "10m", "tests": true}}`,
			contains: []string{
				`"op": "replace"`, `"path": "/run/timeout"`, `"value": "10m"`},
		},
		{
			name:     "key add and remove",
			path:     "a.json",
			base:     `{"a": 1, "b": 2}`,
			want:     `{"a": 1, "c": 3}`,
			contains: []string{`"op": "remove"`, `"path": "/b"`, `"op": "add"`, `"path": "/c"`},
		},
		{
			name:     "array append",
			path:     "a.json",
			base:     `{"list": [1, 2]}`,
			want:     `{"list": [1, 2, 3, 4]}`,
			contains: []string{`"path": "/list/2"`, `"path": "/list/3"`},
		},
		{
			name:     "array truncate removes highest index first",
			path:     "a.json",
			base:     `{"list": [1, 2, 3, 4]}`,
			want:     `{"list": [1]}`,
			contains: []string{`"path": "/list/3"`, `"path": "/list/1"`},
		},
		{
			name:     "array element change",
			path:     "a.json",
			base:     `{"list": [{"x": 1}, {"x": 2}]}`,
			want:     `{"list": [{"x": 1}, {"x": 5}]}`,
			contains: []string{`"path": "/list/1/x"`, `"value": 5`},
		},
		{
			name:     "scalar type change",
			path:     "a.json",
			base:     `{"v": "1"}`,
			want:     `{"v": 1}`,
			contains: []string{`"op": "replace"`, `"value": 1`},
		},
		{
			name:     "container type change",
			path:     "a.json",
			base:     `{"v": [1]}`,
			want:     `{"v": {"k": null}}`,
			contains: []string{`"op": "replace"`, `"value": {`},
		},
		{
			name:     "null and false values are kept",
			path:     "a.json",
			base:     `{"a": 1, "b": 1}`,
			want:     `{"a": null, "b": false}`,
			contains: []string{`"value": null`, `"value": false`},
		},
		{
			name:     "pointer escaping",
			path:     "a.json",
			base:     `{"a/b": 1, "c~d": 2}`,
			want:     `{"a/b": 3, "c~d": 4, "e/~f": 5}`,
			contains: []string{`"/a~1b"`, `"/c~0d"`, `"/e~1~0f"`},
		},
		{
			name:     "yaml nested",
			path:     "a.yaml",
			base:     "run:\n  timeout: 5m\nlinters:\n  - govet\n",
			want:     "run:\n  timeout: 10m\n  tests: true\nlinters:\n  - govet\n  - staticcheck\n",
			contains: []string{`"/run/timeout"`, `"/run/tests"`, `"/linters/1"`},
		},
		{
			name:     "yml truncate",
			path:     "a.yml",
			base:     "linters:\n  - a\n  - b\n  - c\n",
			want:     "linters:\n  - a\n",
			contains: []string{`"/linters/2"`, `"/linters/1"`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			patch := roundTrip(t, tc.path, tc.base, tc.want)
			for _, c := range tc.contains {
				if !strings.Contains(patch, c) {
					t.Errorf("patch lacks %s:\n%s", c, patch)
				}
			}
		})
	}
}

func TestGenerateJSONPatchArrayTruncateOrder(t *testing.T) {
	patch := roundTrip(t, "a.json", `[1,2,3]`, `[1]`)
	if strings.Index(patch, `"/2"`) > strings.Index(patch, `"/1"`) {
		t.Fatalf("removals must go from the highest index downward:\n%s", patch)
	}
}

func TestGenerateJSONPatchDeterministic(t *testing.T) {
	base, want := `{"z": 1, "a": 1, "m": 1}`, `{"z": 2, "a": 2, "m": 2, "b": 0}`
	first := roundTrip(t, "a.json", base, want)
	for i := 0; i < 5; i++ {
		if got := roundTrip(t, "a.json", base, want); got != first {
			t.Fatalf("non-deterministic output:\n%s\n%s", first, got)
		}
	}
	if strings.Index(first, `"/a"`) > strings.Index(first, `"/m"`) ||
		strings.Index(first, `"/m"`) > strings.Index(first, `"/z"`) {
		t.Fatalf("keys not visited in sorted order:\n%s", first)
	}
}

func TestGenerateJSONPatchNoDifference(t *testing.T) {
	patch, err := GenerateJSONPatch("a.json", []byte(`{"a":1}`), []byte("{\n  \"a\": 1\n}\n"))
	if err != nil || string(patch) != "[]\n" {
		t.Fatalf("patch = %q, %v", patch, err)
	}
}

func TestGenerateJSONPatchErrors(t *testing.T) {
	_, err := GenerateJSONPatch("a.json", []byte(`{`), []byte(`{}`))
	wantErr(t, err, "invalid JSON")
	_, err = GenerateJSONPatch("a.json", []byte(`{}`), []byte(`{} {}`))
	wantErr(t, err, "trailing data")
	_, err = GenerateJSONPatch("a.yaml", []byte("a: [\n"), []byte(`{}`))
	wantErr(t, err, "invalid YAML")
	_, err = GenerateJSONPatch("a.txt", []byte(`{}`), []byte(`{}`))
	wantErr(t, err, "file extension")
}

func TestDecodeStructuredUsesNumber(t *testing.T) {
	v, err := DecodeStructured("a.json", []byte(`{"n": 12345678901234567890}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := v.(map[string]any)["n"]; got == nil || reflect.TypeOf(got).String() != "json.Number" {
		t.Fatalf("n = %#v", got)
	}
}

func TestPermittedPatch(t *testing.T) {
	d := mustDescriptor(t, `
[[files]]
path = "a.json"
patch = "json-patch"

[[files]]
path = "cfg/*.yml"
patch = "json-patch"

[[files]]
path = "Makefile"
`)
	got, err := PermittedPatch(tree("a.json", "{}", "cfg/x.yml", "a: 1", "cfg/y.yml", "b: 2", "Makefile", "", "README", ""), d)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"a.json": "json-patch", "cfg/x.yml": "json-patch", "cfg/y.yml": "json-patch", "Makefile": ""}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PermittedPatch = %v, want %v", got, want)
	}
	_, err = PermittedPatch(tree("Makefile", ""), d)
	wantErr(t, err, "does not exist")
}

func TestDecodeStructuredRejectsMultiDocumentYAML(t *testing.T) {
	_, err := DecodeStructured("a.yaml", []byte("a: 1\n---\nb: 2\n"))
	wantErr(t, err, "2 documents")
	// A `---` inside a block scalar is not a document separator.
	v, err := DecodeStructured("a.yaml", []byte("a: |\n  ---\n  text\n"))
	if err != nil || v.(map[string]any)["a"] != "---\ntext\n" {
		t.Fatalf("v = %#v, %v", v, err)
	}
}

func TestDecodeStructuredRejectsDuplicateJSONKeys(t *testing.T) {
	_, err := DecodeStructured("a.json", []byte(`{"a":1,"a":2}`))
	wantErr(t, err, `duplicate object key "a"`)
	_, err = DecodeStructured("a.json", []byte(`{"a":{"b":1},"c":[{"b":1},{"b":2,"b":3}]}`))
	wantErr(t, err, `duplicate object key "b"`)
	// Same key in different objects is fine, as is key/value ambiguity.
	if _, err := DecodeStructured("a.json", []byte(`{"a":{"a":1},"b":[{"a":1},{"a":2}],"c":"a"}`)); err != nil {
		t.Fatal(err)
	}
}
