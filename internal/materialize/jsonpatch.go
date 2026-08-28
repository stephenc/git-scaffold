package materialize

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"sigs.k8s.io/yaml"
	goyaml "sigs.k8s.io/yaml/goyaml.v2"
)

// applyJSONPatch applies an RFC 6902 patch to JSON or YAML content (§26).
// The structural model is chosen by file extension: .json is treated as
// JSON, .yml/.yaml as YAML (parsed to a JSON-compatible model, patched,
// re-serialized as YAML — comments and formatting are not preserved, but the
// output is deterministic). Other extensions are rejected: the content model
// would be a guess, and guessing violates determinism. Operations apply in
// order and the first failure fails materialization, naming the file, the
// patch, and the operation index (§26, §37).
func applyJSONPatch(target, patchPath string, content, patchData []byte) ([]byte, error) {
	patch, err := jsonpatch.DecodePatch(patchData)
	if err != nil {
		return nil, fmt.Errorf("%s: %s: malformed JSON Patch: %v", target, patchPath, err)
	}

	var isYAML bool
	switch {
	case strings.HasSuffix(target, ".json"):
		isYAML = false
	case strings.HasSuffix(target, ".yml"), strings.HasSuffix(target, ".yaml"):
		isYAML = true
	default:
		return nil, fmt.Errorf(
			"%s: %s: json-patch requires a .json, .yml or .yaml file extension", target, patchPath)
	}

	doc := content
	if isYAML {
		if doc, err = yaml.YAMLToJSON(content); err != nil {
			return nil, fmt.Errorf("%s: %s: invalid YAML: %v", target, patchPath, err)
		}
	} else if !json.Valid(content) {
		return nil, fmt.Errorf("%s: %s: invalid JSON in target file", target, patchPath)
	}

	// One operation at a time so a failure names its index (§37).
	for i, op := range patch {
		if doc, err = (jsonpatch.Patch{op}).Apply(doc); err != nil {
			return nil, fmt.Errorf("%s:\n%s: operation %d failed:\n%v", target, patchPath, i, err)
		}
	}

	if isYAML {
		out, err := yaml.JSONToYAML(doc)
		if err != nil {
			return nil, fmt.Errorf("%s: %s: serializing YAML: %v", target, patchPath, err)
		}
		return out, nil
	}
	var out bytes.Buffer
	if err := json.Indent(&out, doc, "", "  "); err != nil {
		return nil, fmt.Errorf("%s: %s: serializing JSON: %v", target, patchPath, err)
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// ApplyJSONPatch is the exported form of applyJSONPatch, so the engine can
// verify a generated json-patch (repatch, init --existing) through exactly
// the applier materialization uses.
func ApplyJSONPatch(target, patchPath string, content, patchData []byte) ([]byte, error) {
	return applyJSONPatch(target, patchPath, content, patchData)
}

// DecodeStructured decodes JSON or YAML content into the generic JSON model
// using the same extension rules as json-patch application: .json is parsed
// as JSON (numbers preserved as json.Number), .yml/.yaml as YAML converted
// to JSON first. Any other extension, or unparsable content, is an error.
// Content that a patch could not faithfully represent is rejected too: a
// YAML stream of more than one document (json-patch sees only the first),
// and JSON objects with duplicate keys.
func DecodeStructured(target string, content []byte) (any, error) {
	doc := content
	switch {
	case strings.HasSuffix(target, ".json"):
		if err := checkDuplicateKeys(content); err != nil {
			return nil, fmt.Errorf("%s: invalid JSON: %v", target, err)
		}
	case strings.HasSuffix(target, ".yml"), strings.HasSuffix(target, ".yaml"):
		n, err := yamlDocumentCount(content)
		if err != nil {
			return nil, fmt.Errorf("%s: invalid YAML: %v", target, err)
		}
		if n > 1 {
			return nil, fmt.Errorf("%s: YAML stream has %d documents; json-patch handles exactly one", target, n)
		}
		if doc, err = yaml.YAMLToJSON(content); err != nil {
			return nil, fmt.Errorf("%s: invalid YAML: %v", target, err)
		}
	default:
		return nil, fmt.Errorf("%s: structured decoding requires a .json, .yml or .yaml file extension", target)
	}
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("%s: invalid JSON: %v", target, err)
	}
	if dec.More() {
		return nil, fmt.Errorf("%s: invalid JSON: trailing data after document", target)
	}
	return v, nil
}

// yamlDocumentCount counts the documents of a YAML stream with a real
// decoder (a `---` regexp would misfire inside block scalars).
func yamlDocumentCount(content []byte) (int, error) {
	dec := goyaml.NewDecoder(bytes.NewReader(content))
	n := 0
	for {
		var v any
		err := dec.Decode(&v)
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return 0, err
		}
		n++
	}
}

// checkDuplicateKeys rejects JSON whose objects repeat a key: encoding/json
// silently keeps the last value, so a patch generated from such a document
// could not reproduce it.
func checkDuplicateKeys(content []byte) error {
	dec := json.NewDecoder(bytes.NewReader(content))
	type frame struct {
		object    bool
		keys      map[string]bool
		expectKey bool
	}
	var stack []*frame
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		top := (*frame)(nil)
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		if top != nil && top.object && top.expectKey {
			// json.Decoder guarantees a string key here.
			if k, ok := tok.(string); ok {
				if top.keys[k] {
					return fmt.Errorf("duplicate object key %q", k)
				}
				top.keys[k] = true
				top.expectKey = false
				continue
			}
		}
		switch d := tok.(type) {
		case json.Delim:
			switch d {
			case '{':
				stack = append(stack, &frame{object: true, keys: map[string]bool{}, expectKey: true})
				continue
			case '[':
				stack = append(stack, &frame{})
				continue
			default:
				stack = stack[:len(stack)-1]
			}
		}
		// A value has completed; an enclosing object now expects a key.
		if len(stack) > 0 && stack[len(stack)-1].object {
			stack[len(stack)-1].expectKey = true
		}
	}
}

// jsonPatchOp is one RFC 6902 operation as emitted by GenerateJSONPatch.
// Value is a pointer so that a legitimate null/false/0 value is kept while
// remove operations carry no value at all.
type jsonPatchOp struct {
	Op    string           `json:"op"`
	Path  string           `json:"path"`
	Value *json.RawMessage `json:"value,omitempty"`
}

// GenerateJSONPatch computes an RFC 6902 patch that transforms base into
// want, both decoded with DecodeStructured under target's extension rules.
// The diff is structural and deterministic: object keys are visited sorted,
// new keys become add, missing keys remove, arrays are compared over their
// common prefix with extra trailing elements added (explicit index) or
// removed from the highest index downward, and any other difference is a
// replace. JSON Pointer segments are escaped per RFC 6901. The output is a
// 2-space indented JSON array with a trailing newline.
func GenerateJSONPatch(target string, base, want []byte) ([]byte, error) {
	from, err := DecodeStructured(target, base)
	if err != nil {
		return nil, err
	}
	to, err := DecodeStructured(target, want)
	if err != nil {
		return nil, err
	}
	ops := []jsonPatchOp{}
	if err := diffJSON("", from, to, &ops); err != nil {
		return nil, fmt.Errorf("%s: %v", target, err)
	}
	out, err := json.MarshalIndent(ops, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("%s: encoding JSON Patch: %v", target, err)
	}
	return append(out, '\n'), nil
}

func rawValue(v any) (*json.RawMessage, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	raw := json.RawMessage(data)
	return &raw, nil
}

func escapePointer(segment string) string {
	return strings.ReplaceAll(strings.ReplaceAll(segment, "~", "~0"), "/", "~1")
}

func diffJSON(path string, from, to any, ops *[]jsonPatchOp) error {
	emit := func(op, p string, v any) error {
		o := jsonPatchOp{Op: op, Path: p}
		if op != "remove" {
			raw, err := rawValue(v)
			if err != nil {
				return err
			}
			o.Value = raw
		}
		*ops = append(*ops, o)
		return nil
	}
	switch f := from.(type) {
	case map[string]any:
		t, ok := to.(map[string]any)
		if !ok {
			return emit("replace", path, to)
		}
		for _, k := range sortedKeys(f) {
			if _, ok := t[k]; !ok {
				if err := emit("remove", path+"/"+escapePointer(k), nil); err != nil {
					return err
				}
			}
		}
		for _, k := range sortedKeys(t) {
			fv, ok := f[k]
			if !ok {
				if err := emit("add", path+"/"+escapePointer(k), t[k]); err != nil {
					return err
				}
				continue
			}
			if err := diffJSON(path+"/"+escapePointer(k), fv, t[k], ops); err != nil {
				return err
			}
		}
		return nil
	case []any:
		t, ok := to.([]any)
		if !ok {
			return emit("replace", path, to)
		}
		n := min(len(f), len(t))
		for i := 0; i < n; i++ {
			if err := diffJSON(fmt.Sprintf("%s/%d", path, i), f[i], t[i], ops); err != nil {
				return err
			}
		}
		for i := n; i < len(t); i++ {
			if err := emit("add", fmt.Sprintf("%s/%d", path, i), t[i]); err != nil {
				return err
			}
		}
		for i := len(f) - 1; i >= n; i-- {
			if err := emit("remove", fmt.Sprintf("%s/%d", path, i), nil); err != nil {
				return err
			}
		}
		return nil
	default:
		if reflect.DeepEqual(from, to) {
			return nil
		}
		return emit("replace", path, to)
	}
}
