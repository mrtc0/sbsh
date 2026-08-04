package builtins

import (
	"context"
	"errors"
	"path"
	"syscall"

	"github.com/mrtc0/sbsh/sandbox/command"
)

// mv moves (renames) files and directories.
//
//	mv source dest
//	mv source... directory
func mv(_ context.Context, inv *command.Invocation) error {
	rest, err := NewFlagSet().Parse(inv.Args)
	if err != nil {
		return command.Exitf(1, "%v", err)
	}
	if len(rest) < 2 {
		return command.Exit(1, "usage: mv source... dest")
	}
	dst := rest[len(rest)-1]
	srcs := rest[:len(rest)-1]

	dstAbs := inv.Abs(dst)
	info, err := inv.FS.Stat(dstAbs)
	dstIsDir := err == nil && info.IsDir()
	if len(srcs) > 1 && !dstIsDir {
		return command.Exitf(1, "target %q is not a directory", dst)
	}

	for _, s := range srcs {
		srcAbs := inv.Abs(s)
		target := dstAbs
		if dstIsDir {
			target = path.Join(dstAbs, path.Base(srcAbs))
		}
		if err := inv.FS.Rename(srcAbs, target); err != nil {
			// A rename across mounts fails with EXDEV (they act as separate filesystems).
			// The raw "invalid cross-device link" is unclear, so reword the cause.
			if errors.Is(err, syscall.EXDEV) {
				return command.Exitf(1, "cannot move %q to %q across mount points", s, dst)
			}
			return command.Exitf(1, "%v", err)
		}
	}
	return nil
}

func init() {
	Register("mv", mv)
}
