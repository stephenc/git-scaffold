package materialize

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"sigs.k8s.io/yaml"
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
