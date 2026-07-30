package vfs

import (
	"errors"
	"os"
	"syscall"

	"github.com/spf13/afero"
)

// FS is the sandbox-wide filesystem handle: a mount router or one of its
// wrappers, i.e. a filesystem whose paths have already been through mount
// resolution. Mount sources are plain afero.Fs — any filesystem can be mounted
// — but everything downstream of Mount takes an FS, so a filesystem that never
// went through the router cannot reach a builtin by accident.
type FS interface {
	afero.Fs

	// Lstater and LinkReader are required rather than optional for the same reason
	// EvalSymlinks is. afero.Walk asks a filesystem for lstat and silently settles
	// for Stat when it is not offered, which answers a question about a link with
	// its target's kind and sends a recursive command into a tree it was not given.
	// A wrapper that forgot to pass lstat down would produce that silently; here,
	// forgetting is a compile error.
	afero.Lstater
	afero.LinkReader

	// EvalSymlinks returns name with every symbolic-link component replaced by
	// its target, as a path in this filesystem's own namespace. A filesystem
	// with no links returns name unchanged.
	//
	// Components that do not exist are left as they are, so a path about to be
	// created is still expressed in terms of the directories it would be created
	// in.
	//
	// It is part of this interface rather than an optional one a caller type-
	// asserts for: a wrapper that forgot to pass resolution down would quietly
	// answer with the unresolved path, and a policy matching that path would let
	// through what it was written to refuse. Here, forgetting is a compile error.
	EvalSymlinks(name string) (string, error)

	// sealed is a marker method to prevent accidental implementation of this interface.
	sealed()
}

func (*VFS) sealed()        {}
func (*ReadOnlyFS) sealed() {}
func (*DenyFS) sealed()     {}

var (
	_ FS = (*VFS)(nil)
	_ FS = (*ReadOnlyFS)(nil)
	_ FS = (*DenyFS)(nil)
)

// errNoLstat reports a filesystem that offers afero's link interfaces but does
// not actually lstat, which leaves a symlink indistinguishable from its target.
var errNoLstat = errors.New("filesystem cannot lstat, so symlinks cannot be told apart")

// lstatIn reports on name in fsys without following a final link, picking how by
// what fsys is able to do. It is the dispatch resolveIn performs, for the one
// question a walk asks.
//
// The bool follows afero's convention: false says the answer came from Stat, so a
// link would have been reported as its target. It can only be false for a
// filesystem that cannot hold a link in the first place.
func lstatIn(fsys afero.Fs, name string) (os.FileInfo, bool, error) {
	if lstater, ok := fsys.(afero.Lstater); ok {
		return lstater.LstatIfPossible(name)
	}
	fi, err := fsys.Stat(name)
	return fi, false, err
}

// readlinkIn returns the target of the link at name in fsys. A filesystem that
// reports no link interface cannot hold one, so the answer is the same EINVAL
// readlink(2) gives for a path that is not a link.
func readlinkIn(fsys afero.Fs, name string) (string, error) {
	if reader, ok := fsys.(afero.LinkReader); ok {
		return reader.ReadlinkIfPossible(name)
	}
	return "", &os.PathError{Op: "readlink", Path: name, Err: syscall.EINVAL}
}

// resolveIn resolves name in fsys, picking how by what fsys is able to do:
//
//  1. an FS resolves itself;
//  2. a filesystem that can lstat and read links is resolved here, by the same
//     walk HostFS uses;
//  3. anything else has no links, so name is already the answer.
//
// Case 3 is not a guess about the implementation. afero's Lstater and LinkReader
// are how a filesystem reports that links exist for it at all, and one that
// implements neither — MemMapFs, for instance — cannot hold one.
//
// Mount sources are plain afero.Fs, which is why this dispatch exists at all;
// everything inside the vfs chain is an FS and takes the first case.
func resolveIn(fsys afero.Fs, name string) (string, error) {
	if f, ok := fsys.(FS); ok {
		return f.EvalSymlinks(name)
	}

	lstater, canLstat := fsys.(afero.Lstater)
	reader, canReadlink := fsys.(afero.LinkReader)
	if !canLstat || !canReadlink {
		return Normalize(name), nil
	}

	return evalSymlinks(
		func(p string) (os.FileInfo, error) {
			fi, lstatUsed, err := lstater.LstatIfPossible(p)
			if err != nil {
				return nil, err
			}
			if !lstatUsed {
				return nil, &os.PathError{Op: "lstat", Path: p, Err: errNoLstat}
			}
			return fi, nil
		},
		reader.ReadlinkIfPossible,
		name,
	)
}
