package builtins

import (
	"context"
	"fmt"
)

// mkdir creates directories. -p creates parent directories too and does not error if they exist.
func mkdir(_ context.Context, env *Env, args []string) error {
	fs := NewFlagSet()
	parents := fs.Bool("-p")
	paths, err := fs.Parse(args)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return fmt.Errorf("usage: mkdir [-p] directory...")
	}
	for _, p := range paths {
		abs := env.Abs(p)
		if *parents {
			if err := env.FS.MkdirAll(abs, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := env.FS.Mkdir(abs, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func init() {
	Register("mkdir", mkdir)
}
