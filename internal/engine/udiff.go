package engine

import (
	"bytes"
	"fmt"
	"strings"
)

// unifiedDiff renders a unified diff (3 context lines) from expected content
// a to working-tree content b for one repo-relative path. bExists
// distinguishes an absent working file from an empty one. Returns "" when
// there is nothing to report.
func unifiedDiff(path string, a, b []byte, bExists bool) string {
	if bExists && bytes.Equal(a, b) {
		return ""
	}
	aName, bName := "a/"+path, "b/"+path
	if !bExists {
		bName = "/dev/null"
	}
	if isBinary(a) || isBinary(b) {
		return fmt.Sprintf("Binary files %s and %s differ\n", aName, bName)
	}
	ops := diffOps(splitLines(a), splitLines(b))
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- %s\n+++ %s\n", aName, bName)
	emitHunks(&sb, ops)
	return sb.String()
}

func isBinary(data []byte) bool {
	return bytes.IndexByte(data, 0) >= 0
}

// splitLines splits into lines that keep their trailing newline; a final
// line without one is preserved as-is and marked when emitted.
func splitLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	var out []string
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			out = append(out, string(data))
			break
		}
		out = append(out, string(data[:i+1]))
		data = data[i+1:]
	}
	return out
}

type diffOp struct {
	kind byte // ' ', '-', '+'
	text string
}

// diffOps computes a line-level edit script via LCS. Inputs beyond the DP
// budget degrade to whole-file replacement, which is still a valid diff.
func diffOps(a, b []string) []diffOp {
	n, m := len(a), len(b)
	if n*m > 4_000_000 {
		ops := make([]diffOp, 0, n+m)
		for _, l := range a {
			ops = append(ops, diffOp{'-', l})
		}
		for _, l := range b {
			ops = append(ops, diffOp{'+', l})
		}
		return ops
	}
	// lcs[i][j] = LCS length of a[i:], b[j:].
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var ops []diffOp
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{' ', a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, diffOp{'-', a[i]})
			i++
		default:
			ops = append(ops, diffOp{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{'-', a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{'+', b[j]})
	}
	return ops
}

const diffContext = 3

func emitHunks(sb *strings.Builder, ops []diffOp) {
	// aAt[k]/bAt[k]: lines of a/b consumed before op k.
	aAt := make([]int, len(ops)+1)
	bAt := make([]int, len(ops)+1)
	for k, op := range ops {
		aAt[k+1], bAt[k+1] = aAt[k], bAt[k]
		if op.kind != '+' {
			aAt[k+1]++
		}
		if op.kind != '-' {
			bAt[k+1]++
		}
	}
	k := 0
	for k < len(ops) {
		if ops[k].kind == ' ' {
			k++
			continue
		}
		start := max(k-diffContext, 0)
		last := k
		for scan := k; scan < len(ops); scan++ {
			if ops[scan].kind != ' ' {
				last = scan
			} else if scan-last > 2*diffContext {
				break
			}
		}
		end := min(last+diffContext, len(ops)-1)
		aStart, aCount := aAt[start], aAt[end+1]-aAt[start]
		bStart, bCount := bAt[start], bAt[end+1]-bAt[start]
		fmt.Fprintf(sb, "@@ -%s +%s @@\n", formatRange(aStart, aCount), formatRange(bStart, bCount))
		for _, op := range ops[start : end+1] {
			sb.WriteByte(op.kind)
			sb.WriteString(op.text)
			if !strings.HasSuffix(op.text, "\n") {
				sb.WriteString("\n\\ No newline at end of file\n")
			}
		}
		k = end + 1
	}
}

// formatRange renders a hunk range: start is the 0-based line offset before
// the hunk; unified diff numbers lines from 1 and keeps the offset when the
// side is empty.
func formatRange(start, count int) string {
	if count == 0 {
		return fmt.Sprintf("%d,0", start)
	}
	if count == 1 {
		return fmt.Sprintf("%d", start+1)
	}
	return fmt.Sprintf("%d,%d", start+1, count)
}
