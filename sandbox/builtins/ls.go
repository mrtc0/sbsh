package builtins

import (
	"context"
	"fmt"
	"sort"

	"github.com/spf13/afero"
)

func ls(_ context.Context, env *Env) error {
	fs := NewFlagSet()
	long := fs.Bool("-l")
	paths, err := fs.Parse(env.Args)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		paths = []string{"."}
	}
	for _, p := range paths {
		info, err := env.FS.Stat(env.Abs(p))
		if err != nil {
			return err
		}
		if !info.IsDir() {
			fmt.Fprintln(env.Stdout, p)
			continue
		}
		entries, err := afero.ReadDir(env.FS, env.Abs(p))
		if err != nil {
			return err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, e := range entries {
			if *long {
				fmt.Fprintf(env.Stdout, "%s %8d %s %s\n",
					e.Mode(), e.Size(), e.ModTime().Format("2006-01-02 15:04"), e.Name())
			} else {
				fmt.Fprintln(env.Stdout, e.Name())
			}
		}
	}
	return nil
}

func init() {
	Register("ls", ls)
}
