package vfs

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// doubleStar is the only segment that matches a variable number of segments.
const doubleStar = "**"

// unsupported lists the glob metacharacters this package deliberately does not
// implement. Rejecting them at parse time keeps the syntax to what is
// documented, and makes path.Match's ErrBadPattern unreachable at match time.
const unsupported = `?[]\`

// Pattern is a parsed path pattern used to select paths in the virtual
// filesystem. Parsing and matching are separate so that callers validate
// patterns once, at construction, rather than on every filesystem operation.
//
// The syntax is deliberately small:
//
//   - "*" matches any run of characters within a single segment. It never
//     matches "/".
//   - "**" matches zero or more whole segments. It must stand alone as a
//     segment.
//   - anything else is a literal.
//
// A pattern that does not start with "/" is anchored at any depth: ".env" is
// shorthand for "**/.env". A pattern that starts with "/" is anchored at the
// root.
type Pattern struct {
	// segs holds the pattern's segments; nil means the pattern is "/", which
	// matches only the root.
	segs []string
}

// ParsePattern parses s. It reports an error for an empty pattern, for the
// metacharacters this package does not support, and for a "**" that shares a
// segment with other characters.
func ParsePattern(s string) (Pattern, error) {
	if s == "" {
		return Pattern{}, errors.New("empty pattern")
	}
	if i := strings.IndexAny(s, unsupported); i >= 0 {
		return Pattern{}, fmt.Errorf("pattern %q: %q is not supported, only * and ** are", s, s[i])
	}
	if !strings.HasPrefix(s, "/") {
		s = doubleStar + "/" + s
	}

	segs := splitPath(path.Clean(s))
	for _, seg := range segs {
		if strings.Contains(seg, doubleStar) && seg != doubleStar {
			return Pattern{}, fmt.Errorf("pattern %q: %q must stand alone as a segment", s, doubleStar)
		}
	}
	return Pattern{segs: segs}, nil
}

// Match reports whether name matches the pattern. name is normalized first, so
// callers may pass either a relative or an absolute path.
func (p Pattern) Match(name string) bool {
	return matchSegments(p.segs, splitPath(Normalize(name)))
}

// canSelectUnder reports whether the pattern can select some path strictly below
// name. It answers from the pattern alone, without touching a filesystem, so a
// caller that would otherwise walk a subtree can skip the walk when no pattern
// reaches into it.
func (p Pattern) canSelectUnder(name string) bool {
	return canSelectUnder(p.segs, splitPath(Normalize(name)))
}

func canSelectUnder(pat, name []string) bool {
	for {
		switch {
		case len(pat) == 0:
			// The pattern is spent: it selects this path or one above it, never
			// one below.
			return false
		case pat[0] == doubleStar:
			// "**" spans whatever is left of name and at least one segment
			// beyond it, and every remaining segment pattern is satisfiable by
			// some name.
			return true
		case len(name) == 0:
			// The pattern still has segments left, and a path below this one can
			// satisfy each of them.
			return true
		}
		// ParsePattern rejected every input that makes path.Match fail.
		if ok, _ := path.Match(pat[0], name[0]); !ok {
			return false
		}
		pat, name = pat[1:], name[1:]
	}
}

// splitPath splits a cleaned absolute path into its segments. The root yields
// no segments.
func splitPath(p string) []string {
	trimmed := strings.Trim(p, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

func matchSegments(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == doubleStar {
			rest := pat[1:]
			// A trailing "**" matches whatever is left, including nothing.
			if len(rest) == 0 {
				return true
			}
			for i := 0; i <= len(name); i++ {
				if matchSegments(rest, name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		// ParsePattern rejected every input that makes path.Match fail.
		ok, _ := path.Match(pat[0], name[0])
		if !ok {
			return false
		}
		pat, name = pat[1:], name[1:]
	}
	return len(name) == 0
}
