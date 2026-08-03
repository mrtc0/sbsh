package builtins

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/mrtc0/sbsh/sandbox/command"
)

// walkGuard lets a command finish its work across entries the deny policy
// refuses. GNU's recursive commands report a refused entry on stderr, cover the
// rest of the tree, and signal the error through the exit code; stopping at the
// first refusal instead lets one denied file disable the whole command.
//
// The guard collects the refusals so the command can pick its exit code once, at
// the end. Which code that is belongs to the command, since GNU does not use one
// value for all of them.
type walkGuard struct {
	inv     *command.Invocation
	refused bool
}

// report writes err to stderr and records that the command's result is
// incomplete, so it can end with a non-zero status once its work is done.
func (g *walkGuard) report(err error) {
	fmt.Fprintf(g.inv.Stderr, "%s: %v\n", g.inv.Name, err)
	g.refused = true
}

// skip reports whether err is the deny policy refusing an entry. When it is, the
// refusal is reported and recorded, and the caller moves on to the rest of its
// work. Every other error is the caller's to handle.
func (g *walkGuard) skip(err error) bool {
	if !errors.Is(err, fs.ErrPermission) {
		return false
	}
	g.report(err)
	return true
}

// wrap adapts fn for afero.Walk so that a refusal is reported and skipped.
//
// Errors fn produces are treated the same as the ones Walk hands in: reading a
// denied file fails inside the body, while stat'ing one fails during traversal,
// and both are the same refusal. fn is called only when Walk reports no error, so
// it can use info without a nil check.
func (g *walkGuard) wrap(fn filepath.WalkFunc) filepath.WalkFunc {
	return func(p string, info os.FileInfo, err error) error {
		if err == nil {
			err = fn(p, info, nil)
		}
		if err == nil || g.skip(err) {
			return nil
		}
		return err
	}
}
