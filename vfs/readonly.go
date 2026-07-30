package vfs

import (
	"os"
	"syscall"
	"time"

	"github.com/spf13/afero"
)

// ReadOnlyFS is an afero.Fs implementation that wraps another afero.Fs and makes it read-only.
// Any write operations (Create, Mkdir, Remove, etc.) will return an error indicating that the filesystem is read-only.
type ReadOnlyFS struct {
	base afero.Fs
}

var _ afero.Fs = (*ReadOnlyFS)(nil)

func NewReadOnlyFS(base afero.Fs) *ReadOnlyFS {
	return &ReadOnlyFS{base: base}
}

func (r *ReadOnlyFS) Name() string { return "ReadOnlyFS(" + r.base.Name() + ")" }

func (r *ReadOnlyFS) Open(name string) (afero.File, error) {
	f, err := r.base.Open(name)
	if err != nil {
		return nil, err
	}
	return readOnlyFile{f}, nil
}

func (r *ReadOnlyFS) Stat(name string) (os.FileInfo, error) {
	return r.base.Stat(name)
}

// EvalSymlinks passes resolution to the base filesystem: making a mount read-only
// says nothing about which file a path names.
func (r *ReadOnlyFS) EvalSymlinks(name string) (string, error) {
	return resolveIn(r.base, name)
}

// LstatIfPossible and ReadlinkIfPossible pass through for the same reason: a
// read-only mount holds the same links a writable one does, and a walk over it has
// to see them as links.
func (r *ReadOnlyFS) LstatIfPossible(name string) (os.FileInfo, bool, error) {
	return lstatIn(r.base, name)
}

func (r *ReadOnlyFS) ReadlinkIfPossible(name string) (string, error) {
	return readlinkIn(r.base, name)
}

func (r *ReadOnlyFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	// If the flag indicates any write operation, return an error indicating that the filesystem is read-only.
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_APPEND|os.O_CREATE|os.O_TRUNC|os.O_EXCL) != 0 {
		return nil, &os.PathError{Op: "open", Path: name, Err: syscall.EROFS}
	}
	f, err := r.base.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return readOnlyFile{f}, nil
}

func (r *ReadOnlyFS) Create(name string) (afero.File, error) {
	return nil, &os.PathError{Op: "create", Path: name, Err: syscall.EROFS}
}

func (r *ReadOnlyFS) Mkdir(name string, perm os.FileMode) error {
	return &os.PathError{Op: "mkdir", Path: name, Err: syscall.EROFS}
}

func (r *ReadOnlyFS) MkdirAll(p string, perm os.FileMode) error {
	return &os.PathError{Op: "mkdir", Path: p, Err: syscall.EROFS}
}

func (r *ReadOnlyFS) Remove(name string) error {
	return &os.PathError{Op: "remove", Path: name, Err: syscall.EROFS}
}

func (r *ReadOnlyFS) RemoveAll(p string) error {
	return &os.PathError{Op: "removeall", Path: p, Err: syscall.EROFS}
}

func (r *ReadOnlyFS) Rename(oldname, newname string) error {
	return &os.LinkError{Op: "rename", Old: oldname, New: newname, Err: syscall.EROFS}
}

func (r *ReadOnlyFS) Chmod(name string, mode os.FileMode) error {
	return &os.PathError{Op: "chmod", Path: name, Err: syscall.EROFS}
}

func (r *ReadOnlyFS) Chown(name string, uid, gid int) error {
	return &os.PathError{Op: "chown", Path: name, Err: syscall.EROFS}
}

func (r *ReadOnlyFS) Chtimes(name string, atime, mtime time.Time) error {
	return &os.PathError{Op: "chtimes", Path: name, Err: syscall.EROFS}
}

// readOnlyFile is a wrapper around afero.File that rejects any write operations with EROFS.
// Even if the underlying Open returns a writable handle (not O_RDONLY),
// this layer ensures that writes from the sandbox are blocked.
type readOnlyFile struct {
	afero.File
}

var _ afero.File = readOnlyFile{}

func (f readOnlyFile) Write(p []byte) (int, error) {
	return 0, &os.PathError{Op: "write", Path: f.Name(), Err: syscall.EROFS}
}

func (f readOnlyFile) WriteAt(p []byte, off int64) (int, error) {
	return 0, &os.PathError{Op: "write", Path: f.Name(), Err: syscall.EROFS}
}

func (f readOnlyFile) WriteString(s string) (int, error) {
	return 0, &os.PathError{Op: "write", Path: f.Name(), Err: syscall.EROFS}
}

func (f readOnlyFile) Truncate(size int64) error {
	return &os.PathError{Op: "truncate", Path: f.Name(), Err: syscall.EROFS}
}
