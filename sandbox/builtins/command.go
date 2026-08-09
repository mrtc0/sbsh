package builtins

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/mrtc0/sh/v3/expand"
	"github.com/mrtc0/sh/v3/interp"

	"github.com/mrtc0/sbsh/sandbox/command"
	"github.com/mrtc0/sbsh/sandbox/exec"
	"github.com/mrtc0/sbsh/sandbox/python"
	"github.com/mrtc0/sbsh/vfs"
)

// shellEnviron adapts the shell's environment to [command.Environ].
type shellEnviron struct{ env expand.Environ }

// NewEnviron returns a [command.Environ] backed by the shell's variables.
func NewEnviron(env expand.Environ) command.Environ { return shellEnviron{env} }

func (s shellEnviron) Lookup(name string) (string, bool) {
	vr := s.env.Get(name)
	if !vr.IsSet() || vr.Kind != expand.String {
		return "", false
	}
	return vr.Str, true
}

func (s shellEnviron) All() []string {
	var out []string
	s.env.Each(func(name string, vr expand.Variable) bool {
		if vr.IsSet() && vr.Kind == expand.String {
			out = append(out, name+"="+vr.Str)
		}
		return true
	})
	return out
}

type Options struct {
	HTTP   *http.Client
	Python python.Interpreter

	// Commands are the commands the host registered from Go, keyed by name. The
	// lookup in resolve is the only thing that tells them from a builtin: they
	// are the same [command.RunFunc], so building the invocation, translating the
	// exit code and reporting "command not found" are shared. The sandbox keeps
	// the two sets of names disjoint, so which is consulted first is not
	// observable.
	Commands map[string]command.Command

	// Nested builds the executor a command reaches nested execution through. It
	// is asked per invocation rather than held once, because what a child
	// inherits — where the command stands, what it can see — is a property of
	// the call and not of the sandbox.
	//
	// It is nil, and so is the executor it may return, when the sandbox offers
	// no nested execution. [command.Invocation.RunNested] answers for that, so
	// nothing here has to.
	Nested func(ctx context.Context, inv *command.Invocation) command.NestedExecutor
}

var registry = map[string]command.RunFunc{}

// Register adds a builtin under name. A builtin is bound by the same return
// contract as a command the host registers: [command.Exit] with a code alone for
// a status of its own — grep's 1 for "no match", diff's for "they differ" — and
// [command.Exit] or [command.Exitf] with a message for a failure the caller
// should be told about. A builtin does not write a diagnostic of its own for the
// failure it returns on: the message travels with the status, and the sandbox
// prints it as "name: message". Writing to Stderr is left to what a builtin
// reports while it keeps working, as [walkGuard] does for a refused entry.
func Register(name string, fn command.RunFunc) { registry[name] = fn }

// Registered reports whether name is a builtin. The sandbox uses it to refuse a
// registration that would shadow one.
func Registered(name string) bool {
	_, ok := registry[name]
	return ok
}

// resolve finds the implementation of name among the builtins and the commands
// the host registered.
func resolve(name string, opts Options) (command.RunFunc, bool) {
	if fn, ok := registry[name]; ok {
		return fn, true
	}
	if cmd, ok := opts.Commands[name]; ok {
		return cmd.Run, true
	}
	return nil, false
}

// ExecMiddleware returns an interp.ExecHandlerFunc that looks up the command in the registry and executes it.
func ExecMiddleware(fsys vfs.FS, opts Options) func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			hc := interp.HandlerCtx(ctx)
			if len(args) == 0 {
				return interp.ExitStatus(0)
			}

			fn, ok := resolve(args[0], opts)
			if !ok {
				// The diagnostic is what a person reads. The error carries the
				// same fact in a form the sandbox can classify: it is a 127 to the
				// shell backend, and an [exec.NotFoundError] to whoever collects
				// the result, which a plain 127 could not be told apart from.
				fmt.Fprintf(hc.Stderr, "%s: command not found\n", args[0])
				return &exec.NotFoundError{Name: args[0]}
			}

			// The one place the shell's handler context is unpacked: the payload a
			// command receives carries no type from the shell backend.
			inv := &command.Invocation{
				Name:   args[0],
				Args:   args[1:],
				Dir:    hc.Dir,
				Stdin:  hc.Stdin,
				Stdout: hc.Stdout,
				Stderr: hc.Stderr,
				FS:     fsys,
				HTTP:   opts.HTTP,
				Python: opts.Python,
				Env:    NewEnviron(hc.Env),
			}
			// After the invocation is built: the executor is handed the payload
			// so that a child starts from where this command stands.
			if opts.Nested != nil {
				inv.Nested = opts.Nested(ctx, inv)
			}
			if err := fn(ctx, inv); err != nil {
				// The single seam between a builtin's int exit code and the
				// shell backend's representation. interp.ExitStatus is a uint8,
				// so the code is reduced modulo 256 here — matching how the OS
				// reports process exit statuses, and in exactly one place.
				var ee *command.ExitError
				if errors.As(err, &ee) {
					// Only the message the payload carries is shown. Text
					// wrapped around it belongs to the wrapper, not to the
					// command, so a status-only exit stays silent even when it
					// reaches here wrapped.
					if ee.Msg != "" {
						fmt.Fprintf(hc.Stderr, "%s: %s\n", args[0], ee.Msg)
					}
					// A zero status is success, and the shell backend has no
					// error to represent it with: interp.ExitStatus(0) is a
					// non-nil error carrying a zero code, which breaks the
					// backend's own invariant. Report success as the absence of
					// an error instead.
					if code := uint8(ee.Code); code != 0 {
						return interp.ExitStatus(code)
					}
					return nil
				}
				// Defensive: a builtin may surface the backend's native exit
				// status directly. It is already a uint8, so pass it through.
				var exitStatus interp.ExitStatus
				if errors.As(err, &exitStatus) {
					return exitStatus
				}
				// Defensive: returning a plain error is outside the contract —
				// there is no status to go by, so the sandbox reports it as a
				// generic failure rather than guessing one.
				fmt.Fprintf(hc.Stderr, "%s: %v\n", args[0], err)
				return interp.ExitStatus(1)
			}
			return nil
		}
	}
}
