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
// Run reports how the script fared in the [NestedRunResult]; a script that fails
// is not a Go error. An error means the request could not be run at all — see
// [NestedExecutor.Run].
type NestedExecutor interface {
	// Run evaluates req.Script as a child execution and blocks until it
	// finishes.
	//
	// The returned error is about the request, not about the script: an empty
	// script, a working directory that is not one, or a nesting depth past
	// [MaxNestingDepth]. Everything the script itself does — a non-zero exit, a
	// command that does not exist, a timeout — is reported in the result, with a
	// nil error.
	Run(ctx context.Context, req NestedRunRequest) (*NestedRunResult, error)
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

// NestedRunResult is what a child execution produced.
//
// ExitCode alone does not say why a script failed, so the reasons the runtime
// knows about are their own fields: a script that exits 127 on purpose and one
// that named a command that does not exist are both 127, and only
// CommandNotFound tells them apart.
type NestedRunResult struct {
	// Stdout and Stderr are what the child wrote, captured in full up to the
	// output limit. Nothing was streamed to the parent's own streams.
	Stdout string
	Stderr string

	// ExitCode is the script's exit status, reduced the way a shell reports one.
	// A child stopped by the sandbox gets the conventional 128+signal status:
	// see [github.com/mrtc0/sbsh/sandbox/exitcode].
	ExitCode int

	// Truncated reports that the output limit cut off stdout or stderr.
	Truncated bool

	// CommandNotFound reports that a command name in the script did not resolve
	// to a builtin or a registered command.
	CommandNotFound bool

	// Denied reports that the sandbox refused the script before running it, so
	// that none of it took effect; DenialReason says why, and the same text is
	// on Stderr. Today the sandbox refuses a script for its syntax — process
	// substitution, which would reach for host resources.
	//
	// It is deliberately narrow. A denial the script runs into part-way — a path
	// the deny patterns cover, a destination outside the network policy — is not
	// reported here: it reaches the command that hit it as an ordinary error,
	// and comes back as that command's exit status and stderr, by which point
	// the rest of the script has run. Widening Denied to cover those means
	// carrying the refusal out of the filesystem and the dialer, which neither
	// is currently able to report, and it is not something to fake by matching
	// on error text. Until then, Denied means "nothing ran", and a caller that
	// needs to know a command was refused mid-script has to read its output.
	Denied       bool
	DenialReason string

	// TimedOut and Canceled report that the child was stopped rather than
	// allowed to finish — because its own or an inherited deadline passed, or
	// because the context was cancelled.
	TimedOut bool
	Canceled bool

	// InternalError is set when the runtime itself failed part-way through the
	// child, which is neither a script failure nor a limit the caller asked for.
	// It is empty in every ordinary outcome.
	InternalError string

	// ExecutionID identifies this child within the sandbox, ParentExecutionID
	// names the execution it hangs off, and Depth is how far from the top-level
	// script it ran. They are what makes a tree of nested executions readable in
	// a log or a test.
	ExecutionID       string
	ParentExecutionID string
	Depth             int
}
