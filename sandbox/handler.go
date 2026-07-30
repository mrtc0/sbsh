package sandbox

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"

	"github.com/mrtc0/sbsh/vfs"
	"github.com/mrtc0/sh/v3/interp"
	"github.com/spf13/afero"
)

type devNull struct{}

func (devNull) Read([]byte) (int, error)    { return 0, io.EOF }
func (devNull) Write(p []byte) (int, error) { return len(p), nil }
func (devNull) Close() error                { return nil }

// absPath is a helper function that returns the absolute path of the given path `p`.
// If `p` is not an absolute path, it joins it with the current working directory from the context.
// The resulting path is normalized using vfs.Normalize.
func absPath(ctx context.Context, p string) string {
	if !path.IsAbs(p) {
		p = path.Join(interp.HandlerCtx(ctx).Dir, p)
	}
	return vfs.Normalize(p)
}

// openHandler returns an interp.OpenHandlerFunc that opens files using the provided vfs.FS.
// It handles the special case of "/dev/null" by returning a devNull implementation.
// If the file cannot be opened, it returns an *os.PathError to indicate a command failure.
func openHandler(fsys vfs.FS) interp.OpenHandlerFunc {
	return func(ctx context.Context, p string, flag int, perm os.FileMode) (io.ReadWriteCloser, error) {
		if p == "/dev/null" {
			return devNull{}, nil
		}
		f, err := fsys.OpenFile(absPath(ctx, p), flag, perm)
		if err != nil {
			var pathErr *os.PathError
			if !errors.As(err, &pathErr) {
				err = &os.PathError{Op: "open", Path: p, Err: err}
			}
			return nil, err
		}
		return f, nil
	}
}

// readDirHandler returns an interp.ReadDirHandlerFunc2 that reads directory entries using the provided vfs.FS.
// It converts the returned fs.FileInfo slice to a slice of fs.DirEntry.
// If the directory cannot be read, it returns an error.
func readDirHandler(fsys vfs.FS) interp.ReadDirHandlerFunc2 {
	return func(ctx context.Context, p string) ([]fs.DirEntry, error) {
		infos, err := afero.ReadDir(fsys, absPath(ctx, p))
		if err != nil {
			return nil, err
		}
		entries := make([]fs.DirEntry, len(infos))
		for i, info := range infos {
			entries[i] = fs.FileInfoToDirEntry(info)
		}
		return entries, nil
	}
}

// statHandler returns an interp.StatHandlerFunc that retrieves file information using the provided vfs.FS.
// It uses the absPath function to get the absolute path of the file.
// If the file cannot be statted, it returns an error.
func statHandler(fsys vfs.FS) interp.StatHandlerFunc {
	return func(ctx context.Context, name string, followSymlinks bool) (fs.FileInfo, error) {
		return fsys.Stat(absPath(ctx, name))
	}
}
