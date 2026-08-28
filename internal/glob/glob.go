// Package glob matches the path-oriented glob syntax of DESIGN.md §9.1:
// `*` matches zero or more non-separator characters, `?` exactly one
// non-separator character, and a `**` path component zero or more complete
// path components. Patterns and paths use `/` regardless of host OS and are
// repository-relative. Matching operates against a provided list of tree
// paths, never the filesystem.
package glob

import (
	"sort"
	"strings"
)

// HasMeta reports whether pattern contains glob metacharacters, i.e. whether
// it is a glob rather than an exact path.
func HasMeta(pattern string) bool {
	return strings.ContainsAny(pattern, "*?")
}

// Match reports whether pattern matches path. Both are `/`-separated
// repository-relative paths. `**` is recursive only as a complete path
// component; inside a component (`a**b`) each `*` matches within that
// component alone.
func Match(pattern, path string) bool {
	return matchComponents(strings.Split(pattern, "/"), strings.Split(path, "/"))
}

// Expand returns the members of paths that pattern matches, sorted
// lexicographically (§9.4: expansion is deterministic and input order must
// not affect materialization).
func Expand(pattern string, paths []string) []string {
	var out []string
	for _, p := range paths {
		if Match(pattern, p) {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func matchComponents(pat, path []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for skip := 0; skip <= len(path); skip++ {
				if matchComponents(pat[1:], path[skip:]) {
					return true
				}
			}
			return false
		}
		if len(path) == 0 || !matchComponent(pat[0], path[0]) {
			return false
		}
		pat, path = pat[1:], path[1:]
	}
	return len(path) == 0
}

// matchComponent matches a single pattern component against a single path
// component. Rune-based so `?` matches one character, not one byte.
func matchComponent(pattern, s string) bool {
	pat, str := []rune(pattern), []rune(s)
	pi, si := 0, 0
	star, mark := -1, 0
	for si < len(str) {
		switch {
		case pi < len(pat) && (pat[pi] == '?' || pat[pi] == str[si]):
			pi++
			si++
		case pi < len(pat) && pat[pi] == '*':
			star, mark = pi, si
			pi++
		case star >= 0:
			pi = star + 1
			mark++
			si = mark
		default:
			return false
		}
	}
	for pi < len(pat) && pat[pi] == '*' {
		pi++
	}
	return pi == len(pat)
}
