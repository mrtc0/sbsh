package repl

import (
	"context"
	"io"
)

// LoopEditor exposes the raw-mode editing loop to the package's tests, which
// drive it over a pipe: allocating a real terminal would make the behaviour that
// matters here—what Ctrl-C does to a line—untestable.
func (r *Runner) LoopEditor(ctx context.Context, sb Executor, rw io.ReadWriter) int {
	return r.loopEditor(ctx, sb, rw)
}
