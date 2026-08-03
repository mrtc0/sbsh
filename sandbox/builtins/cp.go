package builtins

import (
	"context"
	"fmt"
	"os"
	"path"

	"github.com/spf13/afero"

	"github.com/mrtc0/sbsh/vfs"

	"github.com/mrtc0/sbsh/sandbox/command"
)

// cp copies files and directories. -r/-R copies directories recursively.
//
//	cp [-r] source dest
//	cp [-r] source... directory
func cp(_ context.Context, inv *command.Invocation) error {
	fs := NewFlagSet()
	recursive := fs.Bool("-r", "-R")
	rest, err := fs.Parse(inv.Args)
	if err != nil {
		return err
	}
	if len(rest) < 2 {
		return fmt.Errorf("usage: cp [-r] source... dest")
	}
	dst := rest[len(rest)-1]
	srcs := rest[:len(rest)-1]

	dstAbs := inv.Abs(dst)
	dstInfo, dstErr := inv.FS.Stat(dstAbs)
	dstIsDir := dstErr == nil && dstInfo.IsDir()

	if len(srcs) > 1 && !dstIsDir {
		return fmt.Errorf("target %q is not a directory", dst)
	}

	guard := &walkGuard{inv: inv}
	for _, s := range srcs {
		srcAbs := inv.Abs(s)
		info, err := inv.FS.Stat(srcAbs)
		if err != nil {
			if guard.skip(err) {
				continue
			}
			return err
		}
		target := dstAbs
		if dstIsDir {
			target = path.Join(dstAbs, path.Base(srcAbs))
		}
		if info.IsDir() {
			if !*recursive {
				return fmt.Errorf("-r not specified; omitting directory %q", s)
			}
			// info comes from Stat, so a link to a directory lands here. Walking
			// the link itself would find one entry and copy nothing, so the walk
			// starts from what the link names: a source given as an argument is
			// followed, which is what cp does with everything but -P.
			root, err := inv.FS.EvalSymlinks(srcAbs)
			if err != nil {
				if guard.skip(err) {
					continue
				}
				return err
			}
			// A destination inside the source has no end: the walk reads each
			// directory's entries after the copy has created one there, so the copy
			// keeps finding more to copy. GNU cp refuses the same case by name.
			if rel := vfs.Rel(root, target); !path.IsAbs(rel) {
				return fmt.Errorf("cannot copy a directory, %q, into itself, %q", s, dst)
			}
			if err := copyTree(inv, guard, root, target); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(inv.FS, srcAbs, target, info.Mode()); err != nil {
			if guard.skip(err) {
				continue
			}
			return err
		}
	}

	if guard.refused {
		return exit(1)
	}
	return nil
}

func copyFile(fs afero.Fs, src, dst string, mode os.FileMode) error {
	b, err := afero.ReadFile(fs, src)
	if err != nil {
		return err
	}
	return afero.WriteFile(fs, dst, b, mode)
}

func copyTree(inv *command.Invocation, guard *walkGuard, src, dst string) error {
	fs := inv.FS
	return afero.Walk(fs, src, guard.wrap(func(p string, info os.FileInfo, _ error) error {
		// A link found below the source cannot be reproduced: the virtual
		// filesystem has no way to create one. Copying the target under the link's
		// name would put a file in the destination that the source does not have,
		// so the entry is reported and the rest of the tree is copied.
		if info.Mode()&os.ModeSymlink != 0 {
			guard.report(fmt.Errorf("%s: symbolic link not copied", p))
			return nil
		}
		target := path.Join(dst, vfs.Rel(src, p))
		if info.IsDir() {
			// Only the permission bits: a directory's Mode() carries ModeDir as
			// well, and os.Root rejects a perm that is not just the low nine bits.
			return fs.MkdirAll(target, info.Mode().Perm())
		}
		return copyFile(fs, p, target, info.Mode())
	}))
}

func init() {
	Register("cp", cp)
}
