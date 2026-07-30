package vfs

import (
	"os"
	"time"

	"github.com/spf13/afero"
)

// HostFS is an adapter that exposes a host directory as an afero.Fs.
// It delegates to os.Root (similar to openat2 + RESOLVE_BENEATH),
// ensuring that symlinks within the mount cannot escape to the host.
// The only way for the sandbox to reach the host is through this type,
// making it a security boundary for the filesystem.
type HostFS struct {
	// root is the underlying os.Root that is confined to the hostDir.
	root *os.Root
	// dir is the host directory that this HostFS is confined to.
	dir string
}

var (
	_ afero.Fs = (*HostFS)(nil)
	_ FS       = (*HostFS)(nil)
)

func (*HostFS) sealed() {}

// NewHostFS returns a HostFS that is confined to the specified hostDir.
func NewHostFS(hostDir string) (*HostFS, error) {
	r, err := os.OpenRoot(hostDir)
	if err != nil {
		return nil, err
	}
	return &HostFS{root: r, dir: hostDir}, nil
}

func (h *HostFS) Close() error { return h.root.Close() }

// rootRel converts a virtual path (an absolute path normalized by Normalize) to
// a relative path for os.Root.
//
// The virtual path in the VFS is always an absolute path
// like "/foo/bar" (as defined by Normalize).
// On the other hand, os.Root methods only accept relative paths,
// so the leading "/" must be removed to make it relative.
func rootRel(name string) string {
	return Rel("/", name)
}

func (h *HostFS) Name() string { return "HostFS(" + h.dir + ")" }

func (h *HostFS) Create(name string) (afero.File, error) {
	return h.root.Create(rootRel(name))
}

func (h *HostFS) Mkdir(name string, perm os.FileMode) error {
	return h.root.Mkdir(rootRel(name), perm)
}

func (h *HostFS) MkdirAll(p string, perm os.FileMode) error {
	if rootRel(p) == "." {
		return nil
	}
	return h.root.MkdirAll(rootRel(p), perm)
}

func (h *HostFS) Open(name string) (afero.File, error) {
	return h.root.Open(rootRel(name))
}

func (h *HostFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	return h.root.OpenFile(rootRel(name), flag, perm)
}

func (h *HostFS) Remove(name string) error {
	return h.root.Remove(rootRel(name))
}

func (h *HostFS) RemoveAll(p string) error {
	return h.root.RemoveAll(rootRel(p))
}

func (h *HostFS) Rename(oldname, newname string) error {
	return h.root.Rename(rootRel(oldname), rootRel(newname))
}

func (h *HostFS) Stat(name string) (os.FileInfo, error) {
	return h.root.Stat(rootRel(name))
}

func (h *HostFS) Chmod(name string, mode os.FileMode) error {
	return h.root.Chmod(rootRel(name), mode)
}

func (h *HostFS) Chown(name string, uid, gid int) error {
	return h.root.Chown(rootRel(name), uid, gid)
}

func (h *HostFS) Chtimes(name string, atime, mtime time.Time) error {
	return h.root.Chtimes(rootRel(name), atime, mtime)
}

// LstatIfPossible reports on the link itself where name is one, rather than on the
// file it points to.
//
// Traversal and resolution ask different questions of a path. Reading a file
// through a link is what Stat answers, and it is correct. Deciding whether a walk
// descends into an entry is not: answering with the target's kind is what carries a
// recursive command into a tree it was never given, so afero.Walk asks for lstat
// and this is what lets it have one. The bool is afero's, and is always true here.
func (h *HostFS) LstatIfPossible(name string) (os.FileInfo, bool, error) {
	fi, err := h.root.Lstat(rootRel(name))
	if err != nil {
		return nil, false, err
	}
	return fi, true, nil
}

// ReadlinkIfPossible returns the target of the link at name, as it is stored, so a
// relative target stays relative.
func (h *HostFS) ReadlinkIfPossible(name string) (string, error) {
	return h.root.Readlink(rootRel(name))
}

// EvalSymlinks returns name with every symbolic-link component replaced by its
// target, as a path in this filesystem's own namespace.
//
// It exists because a path is not the only name that reaches a file: os.Root
// follows a symlink inside the mount, so a policy that matches path strings needs
// the name the operation will actually act on. Resolution follows os.Root's rules
// — relative targets only, at most eight links, never outside the root — because
// the two have to agree on which file a path names.
func (h *HostFS) EvalSymlinks(name string) (string, error) {
	return evalSymlinks(
		func(p string) (os.FileInfo, error) { return h.root.Lstat(rootRel(p)) },
		func(p string) (string, error) { return h.root.Readlink(rootRel(p)) },
		name,
	)
}
