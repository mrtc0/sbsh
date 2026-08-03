package builtins

import (
	"fmt"
	"path"
	"strings"

	"github.com/mrtc0/sbsh/vfs"
)

// containedPath resolves an untrusted name from an archive or patch against
// base and fails when the result would escape base.
//
// name is always treated as relative: a leading slash is dropped rather than
// honoured, so "/etc/passwd" lands under base — the behaviour GNU tar has when
// extracting absolute members. A name that climbs out with ".." is rejected
// instead, because it has no equally natural reading under base and silently
// relocating it would write somewhere the user never named.
//
// This is builtin policy, not a filesystem primitive: vfs.Rel resolves, this
// confines.
func containedPath(base, name string) (string, error) {
	base = vfs.Normalize(base)
	abs := vfs.Normalize(path.Join(base, name))
	if rel := vfs.Rel(base, abs); path.IsAbs(rel) {
		return "", fmt.Errorf("%q escapes %q", name, base)
	}
	return abs, nil
}

// walkedName turns a VFS absolute path produced by afero.Walk back into the
// display name a user expects, using the argument they typed as the prefix
// (abs must equal inv.Abs(arg), the root the walk started from).
//
// It follows GNU find/grep semantics: the argument is kept verbatim and each
// descendant is joined to it with a single separator. This preserves a typed
// "./" or trailing slash while never doubling separators, so "grep -r p dir"
// and "grep -r p dir/" both print "dir/a" and "find ." prints "./a".
func walkedName(arg, abs, walked string) string {
	if walked == abs {
		return arg
	}
	return strings.TrimSuffix(arg, "/") + "/" + vfs.Rel(abs, walked)
}
