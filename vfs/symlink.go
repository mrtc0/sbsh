package vfs

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"syscall"
)

// maxSymlinks is os.Root's own limit (rootMaxSymlinks in os/root.go). Resolution
// and the operation that follows it have to agree on which paths are too deep,
// so the number is not ours to pick: eight links resolve, the ninth is ELOOP.
const maxSymlinks = 8

// errEscapes reports a link target resolution cannot express within the
// filesystem: one that is absolute, or one that climbs above the root. os.Root
// refuses both instead of resolving them against the root, so resolution refuses
// them too rather than reporting a path that the following operation would not
// open.
var errEscapes = errors.New("path escapes the filesystem root")

// evalSymlinks replaces every symbolic-link component of name with its target.
// lstat recognizes a link and readlink reads it; both take paths in the
// filesystem's own namespace and are expected to confine themselves to it, so
// resolution never checks for an escape itself, it only propagates the error.
//
// Components that do not exist are left as they are: resolution stops at the
// first one lstat reports as missing and re-attaches the remainder untouched, so
// a path being created is still expressed in terms of the directories it would
// be created in.
func evalSymlinks(
	lstat func(string) (os.FileInfo, error),
	readlink func(string) (string, error),
	name string,
) (string, error) {
	rest := splitPath(Normalize(name))
	resolved := make([]string, 0, len(rest))
	links := 0

	for len(rest) > 0 {
		seg, remainder := rest[0], rest[1:]
		rest = remainder

		switch seg {
		case ".":
			continue
		case "..":
			if len(resolved) == 0 {
				return "", &os.PathError{Op: "evalsymlinks", Path: name, Err: errEscapes}
			}
			resolved = resolved[:len(resolved)-1]
			continue
		}

		current := joinSegments(append(resolved, seg))
		fi, err := lstat(current)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				// Nothing under a path that does not exist can be a link.
				return joinSegments(append(append(resolved, seg), rest...)), nil
			}
			return "", err
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			resolved = append(resolved, seg)
			continue
		}

		links++
		if links > maxSymlinks {
			return "", &os.PathError{Op: "evalsymlinks", Path: name, Err: syscall.ELOOP}
		}
		target, err := readlink(current)
		if err != nil {
			return "", err
		}
		if path.IsAbs(target) {
			return "", &os.PathError{Op: "evalsymlinks", Path: current, Err: errEscapes}
		}
		// The target takes the link's place, so what followed the link still
		// follows it, and its own components go through the same checks.
		rest = append(splitPath("/"+target), rest...)
	}

	return joinSegments(resolved), nil
}

// joinSegments builds the absolute path the segments spell out. No segments is
// the root.
func joinSegments(segs []string) string {
	if len(segs) == 0 {
		return "/"
	}
	return "/" + path.Join(segs...)
}
