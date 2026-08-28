package materialize

import (
	"bytes"
	"sort"
)

// substitute performs simultaneous literal token replacement on bytes (§18,
// §21): a single left-to-right scan of the original content, so replacement
// output is never rescanned and order cannot affect the result. When two
// tokens match at the same position the longest wins deterministically (a
// longer literal is the more specific match; ties are impossible because
// tokens are unique within a file). Untouched bytes pass through unchanged.
func substitute(content []byte, tokens map[string]string) []byte {
	if len(tokens) == 0 {
		return content
	}
	// Longest first implements longest-match-wins via first match found.
	keys := make([]string, 0, len(tokens))
	for k := range tokens {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })

	var out bytes.Buffer
	out.Grow(len(content))
	for i := 0; i < len(content); {
		matched := false
		for _, tok := range keys {
			if bytes.HasPrefix(content[i:], []byte(tok)) {
				out.WriteString(tokens[tok])
				i += len(tok)
				matched = true
				break
			}
		}
		if !matched {
			out.WriteByte(content[i])
			i++
		}
	}
	return out.Bytes()
}
