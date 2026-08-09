package sandbox

import (
	"context"
	"strconv"
	"time"
)

// execution is one node of a sandbox's execution tree: a top-level [Sandbox.Exec]
// is a root, and every nested run hangs off the execution that asked for it.
//
// It travels on the context, which is what lets a command dispatched deep inside
// a script find the execution it belongs to without the shell backend having to
// carry a type of its own.
type execution struct {
	// id names this execution within the sandbox, and parentID the one it hangs
	// off — empty for a root.
	id       string
	parentID string

	// depth is 0 for a top-level execution and one more than the parent's for a
	// child. It is what [command.MaxNestingDepth] bounds.
	depth int

	// started is when the execution began, kept so a later change can report
	// how long a child took without threading a clock through the runtime.
	started time.Time

	// outputLimit is how much stdout, and how much stderr, this execution may
	// produce. A child may lower it but never raise it, so the value here is the
	// ceiling for everything below.
	outputLimit int64
}

type executionKey struct{}

// withExecution returns a context that reports exec as the execution running on
// it.
func withExecution(ctx context.Context, exec *execution) context.Context {
	return context.WithValue(ctx, executionKey{}, exec)
}

// executionFrom returns the execution the context belongs to, or nil when the
// context did not come from a sandbox run — a command invoked from a test by
// hand, say.
func executionFrom(ctx context.Context) *execution {
	exec, _ := ctx.Value(executionKey{}).(*execution)
	return exec
}

// newExecution mints a root execution for a top-level [Sandbox.Exec].
func (s *Sandbox) newExecution() *execution {
	return &execution{
		id:          s.nextExecutionID(),
		started:     time.Now(),
		outputLimit: s.outputLimit,
	}
}

// newChildExecution mints an execution hanging off parent, with the output
// budget clamped: limit applies only when it is tighter than what the parent
// already has.
func (s *Sandbox) newChildExecution(parent *execution, limit int64) *execution {
	outputLimit := parent.outputLimit
	if limit > 0 && limit < outputLimit {
		outputLimit = limit
	}
	return &execution{
		id:          s.nextExecutionID(),
		parentID:    parent.id,
		depth:       parent.depth + 1,
		started:     time.Now(),
		outputLimit: outputLimit,
	}
}

// nextExecutionID returns an identifier unique within the sandbox. It counts
// rather than randomizing: the ids end up in results and logs, where being short
// and ordered is worth more than being globally unique.
func (s *Sandbox) nextExecutionID() string {
	return "exec-" + strconv.FormatUint(s.executions.Add(1), 10)
}
