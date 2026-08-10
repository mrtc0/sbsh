package exec

import (
	"context"
	"strconv"

	"github.com/google/uuid"
)

// Execution identifies one run in the sandbox's execution tree. A host calling
// the sandbox starts a root; a command calling back into the sandbox starts a
// child of the execution it is part of.
//
// It travels in the context the run is given, which is what makes the tree
// observable from inside a run: the entry point that admits a run reads the
// context to tell a first call from a re-entrant one, and the nested runtime
// reads it to know how deep it already is.
type Execution struct {
	// ID names the execution. A root's ID is a UUID, so an execution can be
	// named outside the sandbox that ran it — in a log, a trace, a bug report —
	// without a second sandbox in another process having numbered something the
	// same. A child's ID is its parent's with the child's number appended, so
	// "550e8400-e29b-41d4-a716-446655440000.1" is a child of that root and
	// "550e8400-e29b-41d4-a716-446655440000.1.2" is a child of that child.
	//
	// Reading an ID therefore gives the whole ancestry, which is what makes it
	// worth carrying instead of a bare depth: the dotted tail is the path from
	// the root, and the part before the first dot is the run a host asked for.
	ID string

	// Depth is how far the execution is from a root: 0 for a run a host started,
	// 1 for a child of one. It is what the depth guard is checked against.
	Depth int
}

// Child returns the execution for a child of e started as the seq-th execution
// of the sandbox. Numbering children off the sandbox's own sequence rather than
// a count per parent is what keeps an ID unique without the sandbox having to
// remember a counter for every execution that ever ran.
func (e Execution) Child(seq uint64) Execution {
	return Execution{ID: e.ID + "." + strconv.FormatUint(seq, 10), Depth: e.Depth + 1}
}

// Root returns the execution for a run a host started, rather than one started
// from inside the sandbox. Each call mints an ID of its own: a version 4 UUID,
// drawn from crypto/rand, which does not fail — so a root is not created with an
// error to handle, an ID being nothing a caller could do anything about.
func Root() Execution {
	return Execution{ID: uuid.NewString()}
}

// executionKey is the context key the current execution is carried under.
type executionKey struct{}

// NewContext returns ctx carrying e as the execution being run. Everything the
// run reaches — the shell backend, a command it dispatches, a child that command
// starts — is given this context, so the tree is available without threading a
// parameter through the sandbox.
func NewContext(ctx context.Context, e Execution) context.Context {
	return context.WithValue(ctx, executionKey{}, e)
}

// FromContext returns the execution ctx is running under, and whether there is
// one at all. A context with no execution in it belongs to a caller outside the
// sandbox, which is exactly what distinguishes a top-level call from a
// re-entrant one.
func FromContext(ctx context.Context) (Execution, bool) {
	e, ok := ctx.Value(executionKey{}).(Execution)
	return e, ok
}
