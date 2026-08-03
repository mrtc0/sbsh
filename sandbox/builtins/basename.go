package builtins

import (
	"context"
	"fmt"
	"path"
	"strings"
)

// basename strips the directory from a path, printing the final component.
// A second argument, when it is a suffix of the result, is removed.
//
//	basename path [suffix]
func basename(_ context.Context, env *Env) error {
	if len(env.Args) == 0 || len(env.Args) > 2 {
		return fmt.Errorf("usage: basename path [suffix]")
	}

	name := stripTrailingSlashes(env.Args[0])
	if name == "" {
		// A path of only slashes reduces to "/".
		fmt.Fprintln(env.Stdout, "/")
		return nil
	}
	base := path.Base(name)

	if len(env.Args) == 2 {
		suffix := env.Args[1]
		if base != suffix && strings.HasSuffix(base, suffix) {
			base = strings.TrimSuffix(base, suffix)
		}
	}

	fmt.Fprintln(env.Stdout, base)
	return nil
}

// stripTrailingSlashes removes trailing slashes, leaving a single slash for a
// path that is only slashes.
func stripTrailingSlashes(p string) string {
	for len(p) > 1 && strings.HasSuffix(p, "/") {
		p = p[:len(p)-1]
	}
	if p == "/" {
		return ""
	}
	return p
}

func init() {
	Register("basename", basename)
}
