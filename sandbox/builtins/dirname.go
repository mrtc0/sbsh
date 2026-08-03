package builtins

import (
	"context"
	"fmt"
	"path"
)

// dirname prints the directory portion of each path argument, one per line.
//
//	dirname path...
func dirname(_ context.Context, env *Env, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dirname path...")
	}
	for _, p := range args {
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
