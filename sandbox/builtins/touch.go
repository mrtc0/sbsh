package builtins

import (
	"context"
	"fmt"
	"time"

	"github.com/mrtc0/sbsh/sandbox/command"
)

// touch creates an empty file if it does not exist, or updates its modification time to now.
func touch(_ context.Context, inv *command.Invocation) error {
	paths, err := NewFlagSet().Parse(inv.Args)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return fmt.Errorf("usage: touch file...")
	}
	now := time.Now()
	for _, p := range paths {
		abs := inv.Abs(p)
		if _, err := inv.FS.Stat(abs); err == nil {
			if err := inv.FS.Chtimes(abs, now, now); err != nil {
				return err
			}
			continue
		}
		f, err := inv.FS.Create(abs)
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
