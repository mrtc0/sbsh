package builtins

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"syscall"

	"github.com/spf13/afero"
)

// rm removes files and directories. -r recurses; -f does not error when the target is missing.
func rm(_ context.Context, env *Env, args []string) error {
	flags := NewFlagSet()
	recursive := flags.Bool("-r", "-R")
	force := flags.Bool("-f")
	paths, err := flags.Parse(args)
	if err != nil {
		return err
	}
	if len(paths) == 0 && !*force {
		return fmt.Errorf("usage: rm [-r] [-f] file...")
	}

	guard := &walkGuard{env: env}
	for _, p := range paths {
		abs := env.Abs(p)
		info, err := env.FS.Stat(abs)
		if err != nil {
			if guard.skip(err) || (*force && errors.Is(err, fs.ErrNotExist)) {
				continue
			}
			return err
		}
		if info.IsDir() {
			if !*recursive {
				return fmt.Errorf("%q is a directory", p)
			}
			if err := removeTree(env, guard, abs, *force); err != nil {
				return err
			}
			continue
		}
		if err := removeEntry(env, guard, abs, *force); err != nil {
			return err
		}
	}

	if guard.refused {
		return exit(1)
	}
	return nil
}

// removeTree deletes a directory one entry at a time rather than calling
// FS.RemoveAll, so a denied entry costs its own removal instead of the whole
// tree's. That is what GNU rm -r does: it removes what it may, reports what it
// may not, and leaves the parents of a refused entry behind.
//
// Collecting the paths first and deleting them in reverse puts children before
// their parents, the order rmdir requires.
func removeTree(env *Env, guard *walkGuard, root string, force bool) error {
	var paths []string
	err := afero.Walk(env.FS, root, guard.wrap(func(p string, _ os.FileInfo, _ error) error {
		paths = append(paths, p)
		return nil
	}))
	if err != nil {
		return err
	}

	slices.Reverse(paths)
	for _, p := range paths {
		if err := removeEntry(env, guard, p, force); err != nil {
			return err
		}
	}
	return nil
}

// removeEntry deletes one path. A refusal is reported and stepped over, as is a
// directory left non-empty by a refusal below it. A missing path is an error
// unless -f asked for it to be ignored: -f covers what was never there, not what
// the policy refuses, so a refusal stays visible through "rm -rf".
func removeEntry(env *Env, guard *walkGuard, p string, force bool) error {
	err := env.FS.Remove(p)
	switch {
	case err == nil:
		return nil
	case guard.skip(err):
		return nil
	case force && errors.Is(err, fs.ErrNotExist):
		return nil
	case errors.Is(err, syscall.ENOTEMPTY), errors.Is(err, syscall.EEXIST):
		guard.report(err)
		return nil
	default:
		return err
	}
}

func init() {
	Register("rm", rm)
}
