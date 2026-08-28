package materialize

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ApplyTextPatch applies a conventional unified diff to content (§27).
// Application is strict: every hunk must match at exactly the line the hunk
// header names — no fuzz, no offset search, no repair. Any mismatch fails
// materialization, naming the file, the patch, and the hunk.
//
// The applier is hand-rolled rather than a dependency because strictness is
// the requirement: nothing here may drift toward "best effort".
// It is exported so the engine can verify that a generated adoption patch
// (init --existing, §33) round-trips through exactly this applier before it
// is written.
func ApplyTextPatch(target, patchPath string, content, patchData []byte) ([]byte, error) {
	hunks, err := parseUnifiedDiff(patchData)
	if err != nil {
		return nil, fmt.Errorf("%s: %s: %v", target, patchPath, err)
	}

	oldLines, oldEndsNL := splitLines(content)
	var out []string
	outNoEOF := false
	pos := 0 // index into oldLines

	for hi, h := range hunks {
		start := h.oldStart - 1
		if h.oldCount == 0 {
			// Pure insertion: oldStart names the line the insertion follows,
			// so the 0-based insertion point is oldStart itself.
			start = h.oldStart
		}
		if start < pos || start > len(oldLines) {
			return nil, fmt.Errorf("%s:\n%s: hunk %d does not apply: old position %d out of range",
				target, patchPath, hi+1, h.oldStart)
		}
		out = append(out, oldLines[pos:start]...)
		pos = start
		for _, op := range h.ops {
			switch op.kind {
			case ' ', '-':
				if pos >= len(oldLines) || oldLines[pos] != op.text {
					found := "end of file"
					if pos < len(oldLines) {
						found = fmt.Sprintf("%q", oldLines[pos])
					}
					return nil, fmt.Errorf("%s:\n%s: hunk %d failed:\nline %d: expected %q, found %s",
						target, patchPath, hi+1, pos+1, op.text, found)
				}
				if op.kind == ' ' {
					out = append(out, op.text)
					outNoEOF = op.noEOF
				}
				pos++
			case '+':
				out = append(out, op.text)
				outNoEOF = op.noEOF
			}
		}
	}
	if pos < len(oldLines) {
		out = append(out, oldLines[pos:]...)
		outNoEOF = !oldEndsNL
	}

	result := strings.Join(out, "\n")
	if len(out) > 0 && !outNoEOF {
		result += "\n"
	}
	return []byte(result), nil
}

type diffOp struct {
	kind  byte // ' ', '-' or '+'
	text  string
	noEOF bool // a following `\ No newline at end of file` marker
}

type diffHunk struct {
	oldStart, oldCount int
	ops                []diffOp
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

func parseUnifiedDiff(data []byte) ([]diffHunk, error) {
	lines := strings.Split(string(data), "\n")
	var hunks []diffHunk
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		m := hunkHeader.FindStringSubmatch(line)
		if m == nil {
			// Anything outside hunk bodies that is not a hunk header is
			// treated as header/preamble (---/+++/diff/index lines).
			continue
		}
		h := diffHunk{oldStart: atoiDefault(m[1], 0), oldCount: atoiDefault(m[2], 1)}
		newCount := atoiDefault(m[4], 1)
		oldLeft, newLeft := h.oldCount, newCount
		for oldLeft > 0 || newLeft > 0 {
			i++
			if i >= len(lines) {
				return nil, fmt.Errorf("malformed unified diff: truncated hunk %d", len(hunks)+1)
			}
			body := lines[i]
			var op diffOp
			switch {
			case body == "":
				// Tolerate trailing-whitespace-stripped context lines.
				op = diffOp{kind: ' ', text: ""}
			case body[0] == ' ' || body[0] == '-' || body[0] == '+':
				op = diffOp{kind: body[0], text: body[1:]}
			case body[0] == '\\':
				if len(h.ops) == 0 {
					return nil, fmt.Errorf("malformed unified diff: stray %q", body)
				}
				h.ops[len(h.ops)-1].noEOF = true
				continue
			default:
				return nil, fmt.Errorf("malformed unified diff: unexpected line %q in hunk", body)
			}
			switch op.kind {
			case ' ':
				oldLeft--
				newLeft--
			case '-':
				oldLeft--
			case '+':
				newLeft--
			}
			if oldLeft < 0 || newLeft < 0 {
				return nil, fmt.Errorf("malformed unified diff: hunk %d exceeds its header counts", len(hunks)+1)
			}
			h.ops = append(h.ops, op)
		}
		// A `\ No newline` marker may follow the final hunk line.
		if i+1 < len(lines) && strings.HasPrefix(lines[i+1], "\\") {
			h.ops[len(h.ops)-1].noEOF = true
			i++
		}
		hunks = append(hunks, h)
	}
	if len(hunks) == 0 {
		return nil, fmt.Errorf("malformed unified diff: no hunks found")
	}
	return hunks, nil
}

// splitLines splits content into newline-terminated lines, reporting whether
// the final line carried a trailing newline.
func splitLines(content []byte) (lines []string, endsNL bool) {
	if len(content) == 0 {
		return nil, true
	}
	s := string(content)
	endsNL = strings.HasSuffix(s, "\n")
	if endsNL {
		s = s[:len(s)-1]
	}
	return strings.Split(s, "\n"), endsNL
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, _ := strconv.Atoi(s) // the regexp guarantees digits
	return n
}
