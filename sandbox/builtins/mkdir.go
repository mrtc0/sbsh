package builtins

import (
	"context"

	"github.com/mrtc0/sbsh/sandbox/command"
)

// mkdir creates directories. -p creates parent directories too and does not error if they exist.
func mkdir(_ context.Context, inv *command.Invocation) error {
	fs := NewFlagSet()
	parents := fs.Bool("-p")
	paths, err := fs.Parse(inv.Args)
	if err != nil {
		return command.Exitf(1, "%v", err)
	}
	if len(paths) == 0 {
		return command.Exit(1, "usage: mkdir [-p] directory...")
	}
	for _, p := range paths {
		abs := inv.Abs(p)
		if *parents {
			if err := inv.FS.MkdirAll(abs, 0o755); err != nil {
				return command.Exitf(1, "%v", err)
			}
			continue
		}
		if err := inv.FS.Mkdir(abs, 0o755); err != nil {
			return command.Exitf(1, "%v", err)
		}
	}
	return nil
}

func init() {
	Register("mkdir", mkdir)
}
