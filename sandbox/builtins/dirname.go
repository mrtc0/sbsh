package builtins

import (
	"context"
	"fmt"
	"path"

	"github.com/mrtc0/sbsh/sandbox/command"
)

// dirname prints the directory portion of each path argument, one per line.
//
//	dirname path...
func dirname(_ context.Context, inv *command.Invocation) error {
	if len(inv.Args) == 0 {
		return command.Exit(1, "usage: dirname path...")
	}
	for _, p := range inv.Args {
		trimmed := stripTrailingSlashes(p)
		if trimmed == "" {
			fmt.Fprintln(inv.Stdout, "/")
			continue
		}
		fmt.Fprintln(inv.Stdout, path.Dir(trimmed))
	}
	return nil
}

func init() {
	Register("dirname", dirname)
}
