// Package exec is the execution result contract: the one shape an execution in
// the sandbox reports back with, and the rules for how an ending is classified.
//
// It is shared on purpose. Top-level [github.com/mrtc0/sbsh/sandbox.Sandbox.Exec]
// classifies nothing itself: it collects what the run captured and hands the
// ending to [Finish]. The nested execution surface that is being built on top of
// this — a command running in the sandbox invoking the sandbox again — is meant
// to end the same way, so that the two cannot drift apart into separate result
// models.
//
// The contract has two halves. [Result.ExitCode] is what a shell would report,
// so an existing caller keeps working. [Result.Outcome] is why the run ended,
// which is what a caller branches on: a command that was never found, a request
// the sandbox refused, a limit the caller asked for, a failure of the sandbox
// itself. That meaning lives in a field rather than in the wording of a stderr
// line, and stderr stays what a person reads.
//
// # What the contract asks of a nested entry point
//
// The nested entry point a command calls is
// [github.com/mrtc0/sbsh/sandbox/command.Invocation.RunNested], and what runs
// behind it is still being built. What is settled here is what a nested entry
// point has to do, so that a caller cannot tell the two apart by what it is
// handed:
//
//   - End through [Finish], with the context the request ran under. A limit stays
//     the one the caller asked for: a nested run under its parent's deadline
//     reports [OutcomeTimedOut] when that deadline passes, rather than starting a
//     deadline of its own.
//   - Report the outcomes for what a nested request is: [OutcomeInvalid] for a
//     call with no command in it, the way it is a script that does not parse at
//     the top level, [OutcomeDenied] for a call the executor refuses, the way it
//     is refused shell syntax at the top level, and [OutcomeUnsupported] where
//     there is no nested execution to refuse it in the first place.
//   - Fill [Result.Truncated] from what it captured itself. Output limits apply
//     to the run that captured the output.
package exec

import (
	"context"
	"errors"
	"fmt"

	"github.com/mrtc0/sh/v3/interp"

	"github.com/mrtc0/sbsh/sandbox/exitcode"
)

// Outcome is why an execution ended. It is the field a caller branches on,
// rather than matching on the text of a diagnostic.
//
// Every outcome carries an [Result.ExitCode] as well, so a caller that only
// wants a status never has to look at the outcome.
type Outcome int

const (
	// OutcomeCompleted means the request ran to completion and picked its own
	// exit status. It is the outcome of a success and of an ordinary failure
	// alike: a script that exits 1, grep finding no match, a command reporting a
	// bad usage. Nothing stopped the run, so ExitCode is the whole story.
	//
	// Only a run that was left to finish reports it. A run the sandbox stopped
	// reports why it was stopped, whatever status it ended on, so a caller
	// reading OutcomeCompleted knows the status it is looking at is the request's
	// own answer.
	OutcomeCompleted Outcome = iota

	// OutcomeNotFound means a command name did not resolve — it is neither a
	// builtin, nor a command the host registered, and there is no PATH to fall
	// back on. ExitCode is 127.
	//
	// It is reported when the run ended on that unresolved command, which is what
	// [NotFoundError] is for. A script that carries on past one and finishes with a
	// status of its own reports OutcomeCompleted, because that status is what the
	// caller asked for — including when the status it picks is 127.
	OutcomeNotFound

	// OutcomeDenied means the sandbox refused the request rather than running it:
	// shell syntax the sandbox does not allow at the top level, and a nested call
	// the executor refuses. ExitCode is 126, the shell's "cannot execute".
	//
	// A denial is an outcome of a run, not a failure of the sandbox, so no Go
	// error accompanies it and the reason is on Stderr.
	//
	// Denials that a command runs into while it works — a path the sandbox's deny
	// patterns cover, a destination the network policy does not allow — are not this
	// outcome. Those are the command's own failure, and it reports them the way
	// it reports any other: a status and a diagnostic of its own.
	//
	// A request the sandbox has no way to carry out is not this outcome either.
	// That is [OutcomeUnsupported]: "will not" and "cannot" are different
	// answers, and a caller that reports a denial to a user should not be
	// telling them a policy stopped them when the runtime simply cannot do it.
	OutcomeDenied

	// OutcomeTimedOut means the run was stopped because its deadline passed.
	// ExitCode is 137 (128 + SIGKILL), the way a shell reports a killed process.
	//
	// It outranks a status, a successful one included: a run whose deadline
	// passed reports this even when the command it was stopped in returned a
	// status of its own on the way out. The status is what one command made of
	// being interrupted; the deadline is why the run ended.
	OutcomeTimedOut

	// OutcomeCanceled means the run was stopped because the caller's context was
	// cancelled. ExitCode is 130 (128 + SIGINT). Like OutcomeTimedOut it outranks
	// a status the run ended on.
	OutcomeCanceled

	// OutcomeInvalid means the request could never become a run: a script that
	// does not parse at the top level, a nested call with no command in it.
	// ExitCode is 2, the shell's status for a syntax error.
	//
	// The caller passed something the sandbox cannot make sense of, so a Go error
	// accompanies the result and carries the detail.
	OutcomeInvalid

	// OutcomeInternal means the sandbox itself failed, and the run's status says
	// nothing about the request. ExitCode is 125.
	//
	// A Go error accompanies the result. This is the only outcome that reports a
	// fault rather than something the caller asked for or wrote.
	OutcomeInternal

	// OutcomeUnsupported means the request is one this runtime has no way to
	// carry out, whatever its policy says. Calling
	// [github.com/mrtc0/sbsh/sandbox.Sandbox.Exec] from inside the sandbox is the
	// case it exists for: a command doing that would be waiting on the very
	// execution it is part of, so the sandbox answers instead of deadlocking, and
	// nothing about permission is being decided. ExitCode is 126, the same
	// "cannot execute" a denial reports, since a shell has no separate status
	// for it.
	//
	// Like a denial it is an outcome of a run rather than a fault, so no Go error
	// accompanies it and the reason — including what to do instead — is on
	// Stderr. It is the last outcome so that adding it leaves the others' values
	// alone.
	OutcomeUnsupported
)

func (o Outcome) String() string {
	switch o {
	case OutcomeCompleted:
		return "completed"
	case OutcomeNotFound:
		return "command not found"
	case OutcomeDenied:
		return "denied"
	case OutcomeTimedOut:
		return "timed out"
	case OutcomeCanceled:
		return "canceled"
	case OutcomeInvalid:
		return "invalid request"
	case OutcomeInternal:
		return "internal error"
	case OutcomeUnsupported:
		return "unsupported"
	default:
		return fmt.Sprintf("outcome(%d)", int(o))
	}
}

// Result is what one execution in the sandbox reports back, whether it was
// started by a host calling the sandbox or by a command calling back into it.
//
// A Result is always populated, including for the two outcomes that come with a
// Go error: a caller can read Outcome and ExitCode without first checking which
// of the two it was handed.
type Result struct {
	// Stdout and Stderr are what the run captured, up to the sandbox's output
	// limit. Stderr is for a person to read: nothing a caller needs to branch on
	// is only expressed there.
	Stdout string
	Stderr string

	// ExitCode is the status the run reports, reduced modulo 256 the way the OS
	// reports a process exit status. For an outcome other than OutcomeCompleted
	// it is the status the contract fixes for that outcome, not something the
	// request chose.
	ExitCode int

	// Outcome is why the run ended. See [Outcome].
	Outcome Outcome

	// Truncated reports that output passed the sandbox's limit and the rest was
	// discarded. It is independent of the outcome: a run that truncated its
	// output still completed, and a caller that must see everything has to treat
	// truncation as a failure of its own.
	Truncated bool
}

// OK reports whether the request ran to completion and exited zero. It is the
// one check a caller that only cares about "did this work" needs.
func (r *Result) OK() bool { return r.Outcome == OutcomeCompleted && r.ExitCode == 0 }

// Stopped reports whether a limit the caller asked for ended the run, rather
// than the request finishing on its own.
func (r *Result) Stopped() bool {
	return r.Outcome == OutcomeTimedOut || r.Outcome == OutcomeCanceled
}

// Output is what a run captured, as the entry point collected it. It is passed
// to [Finish] so that the streams and the classification are recorded in one
// place.
type Output struct {
	Stdout    string
	Stderr    string
	Truncated bool
}

// Denied returns the result for a request the sandbox refused to run. reason is
// what a person is shown, on Stderr, since no Go error carries it.
func Denied(reason string) *Result {
	return &Result{
		Stderr:   fmt.Sprintf("sbsh: %s\n", reason),
		ExitCode: exitcode.Denied,
		Outcome:  OutcomeDenied,
	}
}

// Unsupported returns the result for a request this runtime has no way to carry
// out. reason is what a person is shown, on Stderr, since no Go error carries
// it; it should say what to do instead, because a caller cannot get this
// answered by asking differently.
func Unsupported(reason string) *Result {
	return &Result{
		Stderr:   fmt.Sprintf("sbsh: %s\n", reason),
		ExitCode: exitcode.Unsupported,
		Outcome:  OutcomeUnsupported,
	}
}

// Invalid returns the result for a request the sandbox cannot make sense of,
// together with the error to return alongside it:
//
//	return exec.Invalid(fmt.Errorf("sandbox: parse: %w", err))
func Invalid(err error) (*Result, error) {
	return &Result{ExitCode: exitcode.Invalid, Outcome: OutcomeInvalid}, err
}

// Internal returns the result for a failure of the sandbox itself, together
// with the error to return alongside it.
func Internal(err error) (*Result, error) {
	return &Result{ExitCode: exitcode.Internal, Outcome: OutcomeInternal}, err
}

// NotFoundError is what dispatch returns for a command name it could not
// resolve. It wraps the shell backend's own status, so the backend treats it as
// an ordinary 127 — the status is not the sandbox failing, and a script may go on
// past it — while the sandbox can still recognize what that 127 was.
//
// Recognizing it is what keeps the fact out of stderr: a caller reads
// [OutcomeNotFound] instead of matching on the wording of a diagnostic. It also
// keeps the fact tied to the command that ended the run: a script that handles
// the unresolved name and then picks 127 for itself ends on a status the backend
// produced, not on this error.
type NotFoundError struct {
	// Name is the name that did not resolve.
	Name string
}

func (e *NotFoundError) Error() string { return fmt.Sprintf("%s: command not found", e.Name) }

// Unwrap is what makes the shell backend see an exit status of 127 rather than a
// failure of the handler, which would abort the script.
func (e *NotFoundError) Unwrap() error { return interp.ExitStatus(exitcode.NotFound) }

// Finish classifies how a run ended and returns its result, together with the
// error the entry point should return alongside it: nil for every ending the
// contract represents in a field, and err itself when the sandbox failed.
//
// err is what running the request returned. Recognized are a name dispatch could
// not resolve, an exit status from the shell backend, an error carrying a status
// of its own (a command's ExitError), and a context that timed out or was
// cancelled. Anything else is a failure of the sandbox: there is no status to go
// by, so guessing one would be worse than saying so.
//
// A run whose context is already done was stopped, and that outranks whatever
// err is: err is only what the run happened to end on once it was being torn
// down.
//
// ctx is the context the request ran under — the one carrying the deadline, not
// the caller's, or a run stopped by the sandbox's own timeout would look like a
// failure of the sandbox.
func Finish(ctx context.Context, out Output, err error) (*Result, error) {
	res := &Result{Stdout: out.Stdout, Stderr: out.Stderr, Truncated: out.Truncated}

	// Being stopped is decided first, and from the context rather than from err:
	// a deadline and a cancellation are limits the caller asked for, so they
	// belong in the result with a nil error, the same way a non-zero exit does.
	//
	// A run that did not finish still ends on something, and what that something
	// is depends on what the sandbox happened to be running: the shell reports
	// the context's own error, but a command that watches for cancellation
	// returns a status of its own — including a zero one, which the shell has no
	// error to represent at all. Taking that status at face value would report
	// the run as having completed and hide the timeout entirely, leaving a caller
	// unable to tell "it failed" from "we never let it finish", or worse, calling
	// an interrupted run a success. The context is the one witness that does not
	// depend on which command noticed first.
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.ExitCode, res.Outcome = exitcode.Timeout, OutcomeTimedOut
		return res, nil
	case errors.Is(err, context.Canceled), errors.Is(ctx.Err(), context.Canceled):
		res.ExitCode, res.Outcome = exitcode.Canceled, OutcomeCanceled
		return res, nil
	}

	if err == nil {
		// The run finished on its own before anything stopped it, so it exited
		// zero however little time it had left.
		return res, nil
	}

	// A name that did not resolve is checked before the status it carries: that
	// status is 127, which on its own says nothing about where the 127 came from.
	var notFound *NotFoundError
	if errors.As(err, &notFound) {
		res.ExitCode, res.Outcome = exitcode.NotFound, OutcomeNotFound
		return res, nil
	}

	if code, ok := statusOf(err); ok {
		res.ExitCode, res.Outcome = code, OutcomeCompleted
		return res, nil
	}

	res.ExitCode, res.Outcome = exitcode.Internal, OutcomeInternal
	return res, err
}

// exitCoder is an error carrying an exit status of its own. command.ExitError
// implements it. Going through an interface is what keeps the dependency one-way:
// the package a command is written against is the one that reaches for this
// contract, not the other way round.
type exitCoder interface {
	ExitCode() int
}

// statusOf extracts the exit status an error carries, in the two shapes the
// sandbox can produce: the backend's own, and a command's when it is what ended
// the run without the backend in between. The backend's is already a uint8; a
// command's is reduced the same way, the way the OS reports a process exit
// status.
func statusOf(err error) (int, bool) {
	var status interp.ExitStatus
	if errors.As(err, &status) {
		return int(status), true
	}
	var coder exitCoder
	if errors.As(err, &coder) {
		return int(uint8(coder.ExitCode())), true
	}
	return 0, false
}
