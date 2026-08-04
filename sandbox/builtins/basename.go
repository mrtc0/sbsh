package builtins

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/mrtc0/sbsh/sandbox/command"
)

// basename strips the directory from a path, printing the final component.
// A second argument, when it is a suffix of the result, is removed.
//
//	basename path [suffix]
func basename(_ context.Context, inv *command.Invocation) error {
	if len(inv.Args) == 0 || len(inv.Args) > 2 {
		return command.Exit(1, "usage: basename path [suffix]")
	}

	name := stripTrailingSlashes(inv.Args[0])
	if name == "" {
		// A path of only slashes reduces to "/".
		fmt.Fprintln(inv.Stdout, "/")
		return nil
	}
	base := path.Base(name)

	if len(inv.Args) == 2 {
		suffix := inv.Args[1]
		if base != suffix && strings.HasSuffix(base, suffix) {
			base = strings.TrimSuffix(base, suffix)
		}
	}

	fmt.Fprintln(inv.Stdout, base)
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
