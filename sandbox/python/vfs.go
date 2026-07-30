package python

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"syscall"
	"time"

	"github.com/spf13/afero"
	expsys "github.com/tetratelabs/wazero/experimental/sys"
	"github.com/tetratelabs/wazero/sys"

	"github.com/mrtc0/sbsh/vfs"
)

// wasmFS is a wrapper adapter that implements the wazero experimental sys.FS interface using a vfs.FS.
type wasmFS struct {
	expsys.UnimplementedFS

	fs vfs.FS
}

var _ expsys.FS = wasmFS{}

func (v wasmFS) OpenFile(name string, flag expsys.Oflag, perm fs.FileMode) (expsys.File, expsys.Errno) {
	f, err := v.fs.OpenFile(name, toOSFlag(flag), perm)
	if err != nil {
		return nil, toErrno(err)
	}
	return &vfsFile{f: f}, 0
}

func (v wasmFS) Stat(name string) (sys.Stat_t, expsys.Errno)  { return v.stat(name) }
func (v wasmFS) Lstat(name string) (sys.Stat_t, expsys.Errno) { return v.stat(name) }

func (v wasmFS) stat(name string) (sys.Stat_t, expsys.Errno) {
	info, err := v.fs.Stat(name)
	if err != nil {
		return sys.Stat_t{}, toErrno(err)
	}
	return toStat(info), 0
}

func (v wasmFS) Mkdir(name string, perm fs.FileMode) expsys.Errno {
	return toErrno(v.fs.Mkdir(name, perm))
}

func (v wasmFS) Rmdir(name string) expsys.Errno  { return toErrno(v.fs.Remove(name)) }
func (v wasmFS) Unlink(name string) expsys.Errno { return toErrno(v.fs.Remove(name)) }

func (v wasmFS) Rename(from, to string) expsys.Errno {
	return toErrno(v.fs.Rename(from, to))
}

func (v wasmFS) Chmod(name string, perm fs.FileMode) expsys.Errno {
	return toErrno(v.fs.Chmod(name, perm))
}

func (v wasmFS) Utimens(name string, atim, mtim int64) expsys.Errno {
	return toErrno(v.fs.Chtimes(name, time.Unix(0, atim), time.Unix(0, mtim)))
}

type vfsFile struct {
	expsys.UnimplementedFile
	f afero.File
}

func (h *vfsFile) Read(buf []byte) (int, expsys.Errno) {
	n, err := h.f.Read(buf)
	if err != nil && err != io.EOF {
		return n, toErrno(err)
	}
	return n, 0
}

func (h *vfsFile) Pread(buf []byte, off int64) (int, expsys.Errno) {
	n, err := h.f.ReadAt(buf, off)
	if err != nil && err != io.EOF {
		return n, toErrno(err)
	}
	return n, 0
}

func (h *vfsFile) Write(buf []byte) (int, expsys.Errno) {
	n, err := h.f.Write(buf)
	if err != nil {
		return n, toErrno(err)
	}
	return n, 0
}

func (h *vfsFile) Pwrite(buf []byte, off int64) (int, expsys.Errno) {
	n, err := h.f.WriteAt(buf, off)
	if err != nil {
		return n, toErrno(err)
	}
	return n, 0
}

func (h *vfsFile) Seek(offset int64, whence int) (int64, expsys.Errno) {
	n, err := h.f.Seek(offset, whence)
	return n, toErrno(err)
}

func (h *vfsFile) Truncate(size int64) expsys.Errno { return toErrno(h.f.Truncate(size)) }
func (h *vfsFile) Sync() expsys.Errno               { return toErrno(h.f.Sync()) }
func (h *vfsFile) Datasync() expsys.Errno           { return toErrno(h.f.Sync()) }
func (h *vfsFile) Close() expsys.Errno              { return toErrno(h.f.Close()) }

func (h *vfsFile) Stat() (sys.Stat_t, expsys.Errno) {
	info, err := h.f.Stat()
	if err != nil {
		return sys.Stat_t{}, toErrno(err)
	}
	return toStat(info), 0
}

func (h *vfsFile) IsDir() (bool, expsys.Errno) {
	info, err := h.f.Stat()
	if err != nil {
		return false, toErrno(err)
	}
	return info.IsDir(), 0
}

func (h *vfsFile) Readdir(n int) ([]expsys.Dirent, expsys.Errno) {
	infos, err := h.f.Readdir(n)
	if err != nil && err != io.EOF {
		return nil, toErrno(err)
	}
	ents := make([]expsys.Dirent, len(infos))
	for i, info := range infos {
		ents[i] = expsys.Dirent{Name: info.Name(), Type: info.Mode().Type()}
	}
	return ents, 0
}

func toOSFlag(f expsys.Oflag) int {
	var o int
	switch f & 0x3 {
	case expsys.O_RDWR:
		o = os.O_RDWR
	case expsys.O_WRONLY:
		o = os.O_WRONLY
	default:
		o = os.O_RDONLY
	}
	if f&expsys.O_APPEND != 0 {
		o |= os.O_APPEND
	}
	if f&expsys.O_CREAT != 0 {
		o |= os.O_CREATE
	}
	if f&expsys.O_EXCL != 0 {
		o |= os.O_EXCL
	}
	if f&expsys.O_TRUNC != 0 {
		o |= os.O_TRUNC
	}
	return o
}

func toStat(info fs.FileInfo) sys.Stat_t {
	return sys.Stat_t{
		Mode:  info.Mode(),
		Size:  info.Size(),
		Nlink: 1,
		Mtim:  info.ModTime().UnixNano(),
	}
}

// toErrno translates a vfs error into the errno wazero hands to the guest.
//
// expsys.Errno is wazero's own enumeration, not a platform errno, so every
// value is written out rather than converted numerically.
//
// The concrete syscall errnos are matched before the fs sentinels because the
// sentinels are the wider set: syscall.ENOTEMPTY satisfies fs.ErrExist and
// syscall.EACCES satisfies fs.ErrPermission, so a sentinel-first order would
// report the vaguer errno for both.
func toErrno(err error) expsys.Errno {
	switch {
	case err == nil:
		return 0

	case errors.Is(err, syscall.EROFS):
		return expsys.EROFS
	case errors.Is(err, syscall.EISDIR):
		return expsys.EISDIR
	case errors.Is(err, syscall.ENOTDIR):
		return expsys.ENOTDIR
	case errors.Is(err, syscall.ENOTEMPTY):
		return expsys.ENOTEMPTY
	case errors.Is(err, syscall.EINVAL):
		return expsys.EINVAL
	case errors.Is(err, syscall.ELOOP):
		return expsys.ELOOP
	case errors.Is(err, syscall.ENAMETOOLONG):
		return expsys.ENAMETOOLONG
	case errors.Is(err, syscall.EXDEV):
		// A cross-mount rename (see vfs.VFS.Rename) is EXDEV, but WASI's errno set
		// has no EXDEV, so it has to borrow another value. EPERM preserves the
		// meaning "this operation cannot be performed": EIO would suggest a
		// hardware fault and EINVAL a malformed argument, and a rename across two
		// filesystems is neither.
		return expsys.EPERM

	case errors.Is(err, fs.ErrNotExist):
		return expsys.ENOENT
	case errors.Is(err, fs.ErrExist):
		return expsys.EEXIST
	case errors.Is(err, fs.ErrPermission):
		// POSIX open(2) reports a denied path as EACCES; stdlib code branches on
		// errno.EACCES, so mapping to EPERM here would misroute that logic.
		return expsys.EACCES
	case errors.Is(err, os.ErrClosed):
		return expsys.EBADF

	default:
		// A deliberate default, not a silent fallback: expsys.Errno is a finite
		// set, so an unrecognized error has to land somewhere. Add a case above
		// when the vfs starts producing an errno that ends up here.
		return expsys.EIO
	}
}
