package builtins

import (
	"context"
	"fmt"
	"sort"

	"github.com/spf13/afero"

	"github.com/mrtc0/sbsh/sandbox/command"
)

func ls(_ context.Context, inv *command.Invocation) error {
	fs := NewFlagSet()
	long := fs.Bool("-l")
	paths, err := fs.Parse(inv.Args)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		paths = []string{"."}
	}
	for _, p := range paths {
		info, err := inv.FS.Stat(inv.Abs(p))
		if err != nil {
			return err
		}
		if !info.IsDir() {
			fmt.Fprintln(inv.Stdout, p)
			continue
		}
		entries, err := afero.ReadDir(inv.FS, inv.Abs(p))
		if err != nil {
			return err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, e := range entries {
			if *long {
				fmt.Fprintf(inv.Stdout, "%s %8d %s %s\n",
					e.Mode(), e.Size(), e.ModTime().Format("2006-01-02 15:04"), e.Name())
			} else {
				fmt.Fprintln(inv.Stdout, e.Name())
			}
		}
	}
	return nil
}

func init() {
	Register("ls", ls)
}
