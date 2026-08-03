package builtins

import (
	"context"

	"github.com/mrtc0/sbsh/sandbox/command"
)

func cat(_ context.Context, inv *command.Invocation) error {
	paths, err := NewFlagSet().Parse(inv.Args)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		paths = []string{"-"}
	}
	for _, p := range paths {
		b, err := readSource(inv, p)
		if err != nil {
			return err
		}
		if _, err := inv.Stdout.Write(b); err != nil {
			return err
		}
	}
	return nil
}

func init() {
	Register("cat", cat)
}
