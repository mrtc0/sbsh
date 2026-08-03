package builtins

import (
	"context"
	"fmt"
	"path"
)

// dirname prints the directory portion of each path argument, one per line.
//
//	dirname path...
func dirname(_ context.Context, env *Env) error {
	if len(env.Args) == 0 {
		return fmt.Errorf("usage: dirname path...")
	}
	for _, p := range env.Args {
		trimmed := stripTrailingSlashes(p)
		if trimmed == "" {
			fmt.Fprintln(env.Stdout, "/")
			continue
		}
		fmt.Fprintln(env.Stdout, path.Dir(trimmed))
	}
	return nil
}

func init() {
	Register("dirname", dirname)
}
