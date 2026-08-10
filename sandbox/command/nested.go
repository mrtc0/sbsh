package command

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mrtc0/sbsh/sandbox/exec"
)

// NestedExecutor runs a script inside the sandbox the calling command is already
// running in. It is what makes a command composable: a command that needs "ls
// /work | sort" done reuses the sandbox's own shell instead of reimplementing
// the pipeline, and it does so without a way out to the host.
//
// A command does not call this interface directly. It calls
// [Invocation.RunNested], which is the entry point: it is the one place a
// request is checked and the one place a sandbox that offers no nested execution
// is answered for. An executor is what the sandbox plugs in behind it.
//
// The run is a child of the execution the command is part of, not a second
// top-level run. It inherits that execution's capabilities — the same
// filesystem and its deny patterns, the same network policy, the same commands
// — and a [NestedRequest] can only narrow what it is given, never widen it. It
// does not inherit the caller's shell state: variables, functions and the
// working directory the child leaves behind are the child's, and the caller's
// execution is unchanged by them. What the child writes to the filesystem is of
// course shared, the way it would be between two commands of a pipeline.
type NestedExecutor interface {
	// Run evaluates req and reports back with the shared execution result
	// contract, so a nested run and a top-level one are told apart by nothing a
	// caller reads. See [Invocation.RunNested] for what the error means.
	Run(ctx context.Context, req NestedRequest) (*exec.Result, error)
}

// NestedRequest is one nested run. Only Script is required: a request that
// leaves the rest zero is the common case, a child that starts where the caller
// stands and under the limits the caller already runs under.
//
// The fields that are limits narrow the run; none of them loosens it. A request
// cannot buy itself more time, more output or a directory the sandbox does not
// otherwise reach, because the point of running in the sandbox is that a command
// cannot hand itself something it was not given.
type NestedRequest struct {
	// Script is the shell script to run, in the same language a host passes to
	// the sandbox: pipelines, redirections and expansion all mean what they mean
	// at the top level.
	//
	// A request with no script in it is not a run, and [Invocation.RunNested]
	// reports [exec.OutcomeInvalid] for it.
	Script string

	// Dir is the working directory the child starts in. It is resolved against
	// the caller's own [Invocation.Dir] when it is relative, and it must stay
	// inside the sandbox filesystem. Empty means the caller's directory, which is
	// what a command that just wants "run this here" leaves it as.
	Dir string

	// Env are the "NAME=value" pairs the child's script reads. A child does not
	// inherit the caller's environment: a variable the script needs is named
	// here or it is unset. That is what keeps a nested run dependent on the
	// request rather than on whatever the calling shell happens to hold.
	//
	// Two variables come without asking. HOME is the caller's, because a script
	// has no other way to find it, and PWD is derived from Dir. Both are the
	// sandbox's to set: an entry naming PWD or OLDPWD is dropped, so PWD cannot
	// disagree with where the child runs and OLDPWD starts unset.
	Env []string

	// Timeout bounds the child on top of whatever time the caller's own
	// execution has left. It cannot extend that: a child never outlives the
	// execution it hangs from, and the deadline that passes first is the one
	// that stops the run — reported as [exec.OutcomeTimedOut] either way.
	//
	// Zero means the child is bounded only by the caller's remaining time.
	Timeout time.Duration

	// OutputLimit is the most output the child may capture, in bytes. Like
	// Timeout it only tightens: a limit above the sandbox's own is the sandbox's.
	// Output past the limit is discarded and [exec.Result.Truncated] says so.
	//
	// Zero means the sandbox's limit.
	OutputLimit int64
}

// RunNested runs a script as a child of the execution this command is part of.
// It is the entry point a command uses; see [NestedExecutor] for what a child
// execution inherits and what it does not.
//
//	res, err := inv.RunNested(ctx, command.NestedRequest{Script: "ls /work | sort"})
//	if err != nil {
//		return command.Exitf(1, "%v", err)
//	}
//	if !res.OK() {
//		return command.Exitf(res.ExitCode, "listing failed: %s", res.Outcome)
//	}
//
// The result is always populated and is the same [exec.Result] a host is handed
// for a top-level run, so a command reads [exec.Result.Outcome] to tell a script
// that failed on its own from one the sandbox refused, stopped, or could not
// make sense of. Following the contract, the error is non-nil only for a request
// the sandbox cannot make sense of and for a failure of the sandbox itself; a
// child that merely exits non-zero is not an error here, any more than it is for
// the host.
//
// A sandbox that offers no nested execution answers every request with
// [exec.OutcomeDenied] rather than a nil-pointer panic, so a command may call
// this without asking first whether it can.
func (inv *Invocation) RunNested(ctx context.Context, req NestedRequest) (*exec.Result, error) {
	if strings.TrimSpace(req.Script) == "" {
		return exec.Invalid(fmt.Errorf("%s: nested request has no script", inv.Name))
	}
	if inv.Nested == nil {
		return exec.Denied("nested execution is not available"), nil
	}
	return inv.Nested.Run(ctx, req)
}
