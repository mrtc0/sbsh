package command

import (
	"context"
	"sync"
)

// unresolvedKey is the context key for the recorder installed by
// [WithUnresolvedRecorder].
type unresolvedKey struct{}

// unresolvedRecorder notes the first command name an execution failed to
// resolve. A pipeline can dispatch from several goroutines at once, hence the
// mutex.
type unresolvedRecorder struct {
	mu   sync.Mutex
	name string
	set  bool
}

// WithUnresolvedRecorder returns a context that collects the command names
// dispatched under it that do not resolve, and a function reading back the first
// one. It is what fills in [ExecutionResult.CommandNotFound]: the dispatcher is
// the only place that knows a name did not resolve, and by the time the script
// has finished all that is left of it is a 127 a script could have produced on
// purpose.
//
// The recorder covers the execution the context belongs to. A nested execution
// installs its own, so a child's unresolved name is not reported as its
// parent's.
func WithUnresolvedRecorder(ctx context.Context) (context.Context, func() (string, bool)) {
	rec := &unresolvedRecorder{}
	return context.WithValue(ctx, unresolvedKey{}, rec), func() (string, bool) {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return rec.name, rec.set
	}
}

// RecordUnresolved notes that name did not resolve to a command. The dispatcher
// calls it; with no recorder installed it does nothing.
func RecordUnresolved(ctx context.Context, name string) {
	rec, ok := ctx.Value(unresolvedKey{}).(*unresolvedRecorder)
	if !ok {
		return
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if !rec.set {
		rec.name, rec.set = name, true
	}
}
