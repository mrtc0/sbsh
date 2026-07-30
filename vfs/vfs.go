package vfs

import (
	"fmt"
	"os"
	"path"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/afero"
)

// Normalize is a utility function that normalizes a given path to ensure it is an absolute path starting with "/" and cleaned of any redundant elements (like "." or "..").
// It is used to maintain consistency in path handling within the virtual filesystem.
func Normalize(p string) string {
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return path.Clean(p)
}

// Rel returns target expressed relative to base, without a leading slash. It is
// the peer of Normalize: both operands are normalized first, so callers may pass
// either display or absolute forms.
//
//   - base == target yields "." (the base itself).
//   - When target is not under base, the normalized target is returned
//     unchanged (an absolute path). afero.Walk never yields such a pair, but Rel
//     is a pure function and defines the case rather than silently mishandling
//     it.
//
// Rel is the inverse in spirit of resolving a path under base; e.g.
// Rel("/", name) is name with its leading slash stripped, matching how HostFS
// turns a VFS path into an os.Root-relative one.
func Rel(base, target string) string {
	base = Normalize(base)
	target = Normalize(target)
	if base == target {
		return "."
	}
	if base == "/" {
		return target[1:]
	}
	if strings.HasPrefix(target, base+"/") {
		return target[len(base)+1:]
	}
	return target
}

// mountEntry represents a single mount point in the VFS.
type mountEntry struct {
	// point is the virtual path where the filesystem is mounted. It must be a normalized absolute path.
	point string
	// fs is the filesystem that is mounted at the point.
	fs afero.Fs
}

// VFS is a virtual filesystem that supports mounting multiple filesystems at different virtual paths.
// It implements the afero.Fs interface and provides a unified view of the mounted filesystems.
//
// e.g., if you mount a filesystem at "/mnt", any access to "/mnt" or its subpaths will be routed to that filesystem.
// Paths that do not match any mount point will be routed to the root filesystem.
type VFS struct {
	mu sync.RWMutex

	// root is the backing FS for "/" and any path that does not match a mount point.
	root afero.Fs
	// mounts is a sorted list of mount points, sorted by descending length of point.
	mounts []mountEntry
}

var _ afero.Fs = (*VFS)(nil)

// NewVFS is a constructor for VFS that takes a root filesystem.
// The root filesystem is used for the root path ("/") and
// any paths that do not match a mount point.
func NewVFS(root afero.Fs) *VFS {
	return &VFS{root: root}
}

// Mount mounts a filesystem at the specified virtual path.
// The virtual path must be a normalized absolute path and cannot be "/".
func (v *VFS) Mount(virtualPath string, fsys afero.Fs) error {
	normalizedVirtualPath := Normalize(virtualPath)
	if normalizedVirtualPath == "/" {
		return fmt.Errorf("cannot mount over root")
	}

	if err := v.MkdirAll(normalizedVirtualPath, 0755); err != nil {
		return fmt.Errorf("failed to create parent directories for mount point %q: %w", normalizedVirtualPath, err)
	}

	v.mu.Lock()
	defer v.mu.Unlock()

	for _, e := range v.mounts {
		if e.point == normalizedVirtualPath {
			return fmt.Errorf("mount point %q already exists", normalizedVirtualPath)
		}
	}

	v.mounts = append(v.mounts, mountEntry{point: normalizedVirtualPath, fs: fsys})

	// Sort the mounts slice in descending order of point length to ensure longest prefix match during resolution.
	slices.SortFunc(v.mounts, func(a, b mountEntry) int {
		return len(b.point) - len(a.point)
	})

	return nil
}

func (v *VFS) resolve(name string) (afero.Fs, string) {
	fsys, _, rel := v.resolveMount(name)
	return fsys, rel
}

// resolveMount is resolve plus the mount point it matched, which a caller needs
// when it has to turn a path from the mount's namespace back into a VFS one. The
// point is empty for a path no mount covers.
func (v *VFS) resolveMount(name string) (fsys afero.Fs, point, rel string) {
	name = Normalize(name)

	v.mu.RLock()
	defer v.mu.RUnlock()

	for _, e := range v.mounts {
		switch {
		case name == e.point:
			return e.fs, e.point, "/"
		case strings.HasPrefix(name, e.point+"/"):
			return e.fs, e.point, name[len(e.point):]
		}
	}

	// If no mount point matches, return the root filesystem and the normalized name.
	return v.root, "", name
}

// EvalSymlinks resolves name within the mount that owns it and puts the mount
// point back, so the answer is a path in the VFS namespace like the one that came
// in.
//
// A link cannot lead out of its mount: each mount is a separate filesystem, and
// the resolution it performs stays inside itself.
func (v *VFS) EvalSymlinks(name string) (string, error) {
	fsys, point, rel := v.resolveMount(name)
	resolved, err := resolveIn(fsys, rel)
	if err != nil {
		return "", err
	}
	if point == "" {
		return resolved, nil
	}
	return Normalize(point + resolved), nil
}

// LstatIfPossible reports on name in the mount that owns it, without following a
// final link. The mount decides whether it can: one that cannot hold a link answers
// from Stat and says so through the bool.
func (v *VFS) LstatIfPossible(name string) (os.FileInfo, bool, error) {
	fsys, rel := v.resolve(name)
	return lstatIn(fsys, rel)
}

// ReadlinkIfPossible returns the target of the link at name as the owning mount
// stores it. The target is not translated into the VFS namespace: it is what
// resolution consumes, and EvalSymlinks is what answers in VFS paths.
func (v *VFS) ReadlinkIfPossible(name string) (string, error) {
	fsys, rel := v.resolve(name)
	return readlinkIn(fsys, rel)
}

func (v *VFS) Name() string { return "VFS" }

func (v *VFS) Create(name string) (afero.File, error) {
	fsys, rel := v.resolve(name)
	return fsys.Create(rel)
}

func (v *VFS) Mkdir(name string, perm os.FileMode) error {
	fsys, rel := v.resolve(name)
	return fsys.Mkdir(rel, perm)
}

func (v *VFS) MkdirAll(p string, perm os.FileMode) error {
	fsys, rel := v.resolve(p)
	return fsys.MkdirAll(rel, perm)
}

func (v *VFS) Open(name string) (afero.File, error) {
	fsys, rel := v.resolve(name)
	return fsys.Open(rel)
}

func (v *VFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	fsys, rel := v.resolve(name)
	return fsys.OpenFile(rel, flag, perm)
}

func (v *VFS) Remove(name string) error {
	fsys, rel := v.resolve(name)
	return fsys.Remove(rel)
}

func (v *VFS) RemoveAll(p string) error {
	// TODO: Recursive deletion is not supported when there are additional mounts under p.
	fsys, rel := v.resolve(p)
	return fsys.RemoveAll(rel)
}

// Rename delegates only within a single mount and returns EXDEV when the
// operation crosses mounts.
//
// rename(2) re-links a directory entry and, by definition, only works within a
// single filesystem (a real kernel also returns EXDEV when crossing devices).
// Each mount is an independent afero.Fs, i.e. the equivalent of a separate
// device, so ofs.Rename cannot act on nrel, a path belonging to another fs.
// Emulating a cross-mount move with copy+delete is possible, but it (1) is
// non-atomic and breaks rename's core guarantee (the safe write-temp→rename
// swap), and (2) opens a hole for moving data across the RO/RW mount boundary.
// So, like a real kernel, we return EXDEV and leave the copy+delete fallback to
// the callers that need it.
func (v *VFS) Rename(oldname, newname string) error {
	ofs, orel := v.resolve(oldname)
	nfs, nrel := v.resolve(newname)
	if ofs != nfs {
		return &os.LinkError{Op: "rename", Old: oldname, New: newname, Err: syscall.EXDEV}
	}
	return ofs.Rename(orel, nrel)
}

func (v *VFS) Stat(name string) (os.FileInfo, error) {
	fsys, rel := v.resolve(name)
	return fsys.Stat(rel)
}

func (v *VFS) Chmod(name string, mode os.FileMode) error {
	fsys, rel := v.resolve(name)
	return fsys.Chmod(rel, mode)
}

func (v *VFS) Chown(name string, uid, gid int) error {
	fsys, rel := v.resolve(name)
	return fsys.Chown(rel, uid, gid)
}

func (v *VFS) Chtimes(name string, atime, mtime time.Time) error {
	fsys, rel := v.resolve(name)
	return fsys.Chtimes(rel, atime, mtime)
}
