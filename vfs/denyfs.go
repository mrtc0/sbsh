package vfs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"syscall"
	"time"

	"github.com/spf13/afero"
)

// DenyFS wraps a filesystem and refuses every operation on a path selected by
// one of its patterns, reporting EACCES.
//
// It layers on top of mount resolution rather than replacing it: mounts decide
// which host directories are visible and whether they are writable, and the
// patterns carve exceptions out of what is left. A pattern that matches a
// directory also covers everything below it, so denying "/work/secrets" is
// enough to protect the whole subtree.
//
// Denied entries are not hidden from directory listings. Their existence is
// still observable, exactly as a file with no permission bits is on a real
// system; only access to them fails. Hiding them would make the sandbox report
// that a file does not exist when it does, and invite callers to create it.
type DenyFS struct {
	base     FS
	patterns []Pattern
}

var _ afero.Fs = (*DenyFS)(nil)

// NewDenyFS wraps base so that paths matching any of the patterns are refused.
// It reports an error if a pattern cannot be parsed; callers are expected to
// fail startup rather than run with a policy that was only partly understood.
func NewDenyFS(base FS, patterns []string) (*DenyFS, error) {
	parsed := make([]Pattern, 0, len(patterns))
	for _, s := range patterns {
		p, err := ParsePattern(s)
		if err != nil {
			return nil, fmt.Errorf("deny path: %w", err)
		}
		parsed = append(parsed, p)
	}
	return &DenyFS{base: base, patterns: parsed}, nil
}

// denied reports whether name is refused, judging both the path as given and the
// path it resolves to.
//
// Both are needed. Matching only the given path lets a symlink stand in for a
// denied file, since a pattern selects names and a link is another name for the
// same file. Matching only the resolved path lets a denied name through whenever
// it happens to be a link to something no pattern selects.
//
// A name that cannot be resolved is refused: which file it reaches is then
// unknown, and the policy fails closed. Resolution is skipped when there are no
// patterns, and when the given path is already refused, so an operation only pays
// for it where the answer could still change.
func (d *DenyFS) denied(name string) bool {
	if len(d.patterns) == 0 {
		return false
	}
	if d.deniedPath(name) {
		return true
	}

	resolved, err := d.base.EvalSymlinks(name)
	if err != nil {
		return true
	}
	if resolved == Normalize(name) {
		return false
	}
	return d.deniedPath(resolved)
}

// deniedPath reports whether p, or any directory above it, is selected by a
// pattern. Walking up means a denied directory covers its whole subtree without
// the caller having to spell out a "/**" suffix.
func (d *DenyFS) deniedPath(name string) bool {
	p := Normalize(name)
	for {
		for _, pat := range d.patterns {
			if pat.Match(p) {
				return true
			}
		}
		if p == "/" {
			return false
		}
		p = path.Dir(p)
	}
}

// errDeniedInSubtree stops a subtree walk at the first selected path. The walk
// answers a yes-or-no question, so visiting the rest adds nothing.
var errDeniedInSubtree = errors.New("denied path below the target")

// deniedUnder reports whether any path strictly below name is selected by a
// pattern. RemoveAll and Rename act on a whole subtree, so checking name alone
// would let a caller delete a denied file by naming one of its parents, or carry
// it out of the patterns' reach by renaming an ancestor.
//
// The patterns are consulted first. When none of them reaches below name there is
// nothing to look for, and the subtree is left unwalked. The walk itself runs on
// the base filesystem: this layer refuses to stat the very entries it needs to
// find.
//
// A path that does not exist holds nothing and is not denied, so RemoveAll on one
// still succeeds as it does on a real filesystem. Any other failure leaves the
// subtree's contents unknown and is reported as denied.
func (d *DenyFS) deniedUnder(name string) bool {
	p := Normalize(name)
	if !slices.ContainsFunc(d.patterns, func(pat Pattern) bool { return pat.canSelectUnder(p) }) {
		return false
	}

	found := false
	err := afero.Walk(d.base, p, func(q string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if Normalize(q) == p {
			return nil
		}
		for _, pat := range d.patterns {
			if pat.Match(q) {
				found = true
				return errDeniedInSubtree
			}
		}
		return nil
	})

	switch {
	case found:
		return true
	case err == nil, errors.Is(err, fs.ErrNotExist):
		return false
	default:
		return true
	}
}

func eaccesPath(op, name string) error {
	return &os.PathError{Op: op, Path: name, Err: syscall.EACCES}
}

func (d *DenyFS) Name() string { return "DenyFS(" + d.base.Name() + ")" }

// EvalSymlinks passes resolution to the base filesystem. The deny layer decides
// what may be reached, not which file a path names, and a caller asking where a
// path leads is not yet acting on it.
func (d *DenyFS) EvalSymlinks(name string) (string, error) {
	return d.base.EvalSymlinks(name)
}

// LstatIfPossible goes through the same check as Stat. A pattern selects a name and
// lstat reports that name's own mode, size and kind, so passing it through would
// answer for a path the policy refuses.
func (d *DenyFS) LstatIfPossible(name string) (os.FileInfo, bool, error) {
	if d.denied(name) {
		return nil, false, eaccesPath("lstat", name)
	}
	return d.base.LstatIfPossible(name)
}

// ReadlinkIfPossible is not checked, unlike LstatIfPossible. Reading a target is
// part of working out which file a path names, which is what denied itself relies
// on; gating it would make the decision depend on its own outcome. A target is a
// stored string, not access to what it points to, and every operation that follows
// it is checked.
func (d *DenyFS) ReadlinkIfPossible(name string) (string, error) {
	return d.base.ReadlinkIfPossible(name)
}

func (d *DenyFS) Open(name string) (afero.File, error) {
	if d.denied(name) {
		return nil, eaccesPath("open", name)
	}
	return d.base.Open(name)
}

func (d *DenyFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if d.denied(name) {
		return nil, eaccesPath("open", name)
	}
	return d.base.OpenFile(name, flag, perm)
}

func (d *DenyFS) Create(name string) (afero.File, error) {
	if d.denied(name) {
		return nil, eaccesPath("create", name)
	}
	return d.base.Create(name)
}

func (d *DenyFS) Stat(name string) (os.FileInfo, error) {
	if d.denied(name) {
		return nil, eaccesPath("stat", name)
	}
	return d.base.Stat(name)
}

func (d *DenyFS) Mkdir(name string, perm os.FileMode) error {
	if d.denied(name) {
		return eaccesPath("mkdir", name)
	}
	return d.base.Mkdir(name, perm)
}

func (d *DenyFS) MkdirAll(p string, perm os.FileMode) error {
	if d.denied(p) {
		return eaccesPath("mkdir", p)
	}
	return d.base.MkdirAll(p, perm)
}

func (d *DenyFS) Remove(name string) error {
	if d.denied(name) {
		return eaccesPath("remove", name)
	}
	return d.base.Remove(name)
}

// RemoveAll refuses when the subtree holds a denied path. Deleting is a write, so
// a pattern that closes a file closes its removal too, and a recursive delete
// must not become the way around that.
func (d *DenyFS) RemoveAll(p string) error {
	if d.denied(p) || d.deniedUnder(p) {
		return eaccesPath("removeall", p)
	}
	return d.base.RemoveAll(p)
}

func (d *DenyFS) Chmod(name string, mode os.FileMode) error {
	if d.denied(name) {
		return eaccesPath("chmod", name)
	}
	return d.base.Chmod(name, mode)
}

func (d *DenyFS) Chown(name string, uid, gid int) error {
	if d.denied(name) {
		return eaccesPath("chown", name)
	}
	return d.base.Chown(name, uid, gid)
}

func (d *DenyFS) Chtimes(name string, atime, mtime time.Time) error {
	if d.denied(name) {
		return eaccesPath("chtimes", name)
	}
	return d.base.Chtimes(name, atime, mtime)
}

// Rename checks both ends: a denied path must not be readable by moving it out,
// nor writable by moving something onto it.
//
// Both subtrees are checked as well. A move relocates everything below the name,
// and an anchored pattern selects paths, not contents, so renaming an ancestor of
// a denied directory would leave its files outside every pattern and readable
// under the new name.
func (d *DenyFS) Rename(oldname, newname string) error {
	if d.denied(oldname) || d.denied(newname) || d.deniedUnder(oldname) || d.deniedUnder(newname) {
		return &os.LinkError{Op: "rename", Old: oldname, New: newname, Err: syscall.EACCES}
	}
	return d.base.Rename(oldname, newname)
}
