package builtins

import (
	"context"
	"fmt"
	"time"
)

// touch creates an empty file if it does not exist, or updates its modification time to now.
func touch(_ context.Context, env *Env) error {
	paths, err := NewFlagSet().Parse(env.Args)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return fmt.Errorf("usage: touch file...")
	}
	now := time.Now()
	for _, p := range paths {
		abs := env.Abs(p)
		if _, err := env.FS.Stat(abs); err == nil {
			if err := env.FS.Chtimes(abs, now, now); err != nil {
				return err
			}
			continue
		}
		f, err := env.FS.Create(abs)
		if err != nil {
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	return nil
}

func init() {
	Register("touch", touch)
}
