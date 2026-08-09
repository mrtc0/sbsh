package command

import (
	"context"
	"errors"
	"time"
)

// MaxNestingDepth is how deep nested execution may go. A top-level script runs
// at depth 0, the child it starts at depth 1, and so on; a request that would
// exceed this depth fails with [ErrMaxDepth].
//
// The limit exists because nothing else stops a command from calling itself:
// there is no separate process per execution to run out of, so a runaway
// recursion would grow the Go stack until the program died. The value is
// deliberately small — a command graph that needs more than a handful of levels
// is better expressed as a flatter one.
const MaxNestingDepth = 8

// ErrMaxDepth is returned by [NestedExecutor.Run] when the request would nest
// deeper than [MaxNestingDepth].
var ErrMaxDepth = errors.New("nested execution: maximum nesting depth reached")

// NestedExecutor runs a shell script as a child of the execution the command is
// part of, within the same sandbox.
//
// A child execution sees the same filesystem, the same network policy and the
// same set of commands as its parent: nested execution is not a way to reach
// past the sandbox boundary, and it never runs a host process. What it does not
// share is interpreter state — the child starts with a fresh shell, inheriting
// the parent's working directory and its exported variables and nothing else, so
// variables it sets, functions it defines and directories it changes into are
// gone when it returns. Writes to the filesystem, of course, are not.
//
// Run reports how the script fared in an [ExecutionResult] — the same type, with
// the same meanings, that a host gets back from running a script from outside.
// A script that fails is not a Go error; an error means the request could not be
// run at all. See [NestedExecutor.Run].
type NestedExecutor interface {
	// Run evaluates req.Script as a child execution and blocks until it
	// finishes.
	//
	// The returned error is about the request, not about the script: an empty
	// script, a working directory that is not one, or a nesting depth past
	// [MaxNestingDepth]. Everything the script itself does — a non-zero exit, a
	// command that does not exist, a refusal, a timeout — is reported in the
	// result, with a nil error. A command reports to a shell and has no error
	// channel to the host, which is why that boundary sits further over here
	// than it does for a host calling the sandbox from outside.
	Run(ctx context.Context, req NestedRunRequest) (*ExecutionResult, error)
}

// NestedRunRequest is one nested execution to run.
//
// The zero value beside Script is the useful one: it runs the script where the
// parent is, with what the parent can see, under what is left of the parent's
// time and output budget.
type NestedRunRequest struct {
	// Script is the shell script to evaluate. It is required.
	Script string

	// Dir is the working directory to start the child in. Empty inherits the
	// calling command's working directory; a relative path is resolved against
	// it, the way [Invocation.Abs] resolves an argument. It has to name a
	// directory that exists in the sandbox filesystem, or the request fails.
	Dir string

	// Env are "NAME=value" pairs layered over what the child inherits, so an
	// entry here adds a variable or replaces one. PWD always follows the working
	// directory and cannot be set from here.
	//
	// What the child inherits is the calling command's exported variables, and
	// only those: a child is a fresh shell, and a variable the parent script set
	// without exporting is not something a child shell would see. Everything
	// listed here is exported in the child, so it carries on down.
	Env []string

	// Timeout bounds the child on its own. It cannot extend the parent's
	// deadline, only bring one closer: a child that outlives its parent's budget
	// is stopped when the parent's is. Zero leaves the parent's budget as the
	// only bound.
	Timeout time.Duration

	// OutputLimit is how many bytes of stdout and of stderr the child may
	// produce before the rest is dropped. It is clamped to the parent's limit,
	// so it can only tighten it. Zero leaves the parent's limit in force.
	OutputLimit int64
}
