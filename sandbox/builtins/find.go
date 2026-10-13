package builtins

import (
	"context"
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/mrtc0/sh/v3/pattern"
	"github.com/spf13/afero"

	"github.com/mrtc0/sbsh/sandbox/command"
)

// find walks the given paths recursively and prints paths matching the conditions.
//
//	-name PATTERN  base name matches the glob / -path PATTERN  whole printed
//	path matches the glob / -type f|d  filter by file kind
//	find [path...] [-name pattern] [-path pattern] [-type f|d]
//
// Conditions are ANDed, as in GNU find with no explicit operators.
func find(_ context.Context, inv *command.Invocation) error {
	var roots []string
	var namePat, typ string
	var pathPat *string
	for i := 0; i < len(inv.Args); i++ {
		a := inv.Args[i]
		switch a {
		case "-name":
			i++
			if i >= len(inv.Args) {
				return command.Exit(1, "-name requires an argument")
			}
			namePat = inv.Args[i]
		case "-path":
			i++
			if i >= len(inv.Args) {
				return command.Exit(1, "-path requires an argument")
			}
			// A pointer, not a string: -path "" is a real condition that
			// matches nothing, which an empty string cannot express.
			pat := inv.Args[i]
			pathPat = &pat
		case "-type":
			i++
			if i >= len(inv.Args) {
				return command.Exit(1, "-type requires an argument")
			}
			typ = inv.Args[i]
			if typ != "f" && typ != "d" {
				return command.Exitf(1, "invalid -type %q (want f or d)", typ)
			}
		default:
			if strings.HasPrefix(a, "-") {
				return command.Exitf(1, "unknown option: %s", a)
			}
			roots = append(roots, a)
		}
	}
	if len(roots) == 0 {
		roots = []string{"."}
	}

	// Compiled once, before the walk: a bad pattern is a usage error, not
	// something to report per entry.
	var pathRe *regexp.Regexp
	if pathPat != nil {
		re, err := compilePathPattern(*pathPat)
		if err != nil {
			return command.Exitf(1, "invalid -path %q: %v", *pathPat, err)
		}
		pathRe = re
	}

	guard := &walkGuard{inv: inv}
	for _, root := range roots {
		abs := inv.Abs(root)
		err := afero.Walk(inv.FS, abs, guard.wrap(func(p string, info os.FileInfo, _ error) error {
			if typ == "f" && info.IsDir() {
				return nil
			}
			if typ == "d" && !info.IsDir() {
				return nil
			}
			if namePat != "" {
				ok, err := path.Match(namePat, path.Base(p))
				if err != nil {
					return err
				}
				if !ok {
					return nil
				}
			}
			name := walkedName(root, abs, p)
			// A directory that does not match is still descended into: -path
			// filters what is printed, it does not prune the walk.
			if pathRe != nil && !pathRe.MatchString(name) {
				return nil
			}
			fmt.Fprintln(inv.Stdout, name)
			return nil
		}))
		if err != nil {
			return command.Exitf(1, "%v", err)
		}
	}

	if guard.refused {
		return command.Exit(1)
	}
	return nil
}

// compilePathPattern turns a -path glob into a matcher for the whole printed
// path, the way GNU find matches it: the pattern is compared against the name
// as find would print it, with no absolutising or normalising of either side.
//
// Wildcards are unrestricted — "*" and "?" match "/", a leading ".", and "\n"
// alike, so "*.txt" matches "./sub/a.txt" — and "**" is just two stars in a
// row. Extended operators such as "!(x)" are not pattern syntax here; they are
// matched literally.
func compilePathPattern(pat string) (*regexp.Regexp, error) {
	expr, err := pattern.Regexp(pat, pattern.EntireString|pattern.NoGlobStar)
	if err != nil {
		return nil, err
	}
	return regexp.Compile(expr)
}

func init() {
	Register("find", find)
}
