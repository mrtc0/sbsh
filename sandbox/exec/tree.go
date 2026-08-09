package exec

import (
	"context"
	"strconv"
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
	// ID names the execution within the sandbox. Executions are numbered in the
	// order they start, and a child's ID is its parent's with the child's number
	// appended: "3" is the third execution the sandbox ran, and "3.5" is the
	// fifth, started from inside the third. Reading an ID therefore gives the
	// whole ancestry, which is what makes it worth carrying instead of a bare
	// depth.
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

// Root returns the execution for the seq-th execution of the sandbox, started by
// a host rather than from inside the sandbox.
func Root(seq uint64) Execution {
	return Execution{ID: strconv.FormatUint(seq, 10)}
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
