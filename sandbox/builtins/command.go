package builtins

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/mrtc0/sh/v3/expand"
	"github.com/mrtc0/sh/v3/interp"

	"github.com/mrtc0/sbsh/sandbox/command"
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

// exit reports a non-zero exit code from a builtin. It is [command.Exit], so a
// builtin and a registered command report a status the same way and
// ExecMiddleware has one representation to translate.
func exit(code int) error { return command.Exit(code) }

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
}

var registry = map[string]command.RunFunc{}

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
				fmt.Fprintf(hc.Stderr, "%s: command not found\n", args[0])
				return interp.ExitStatus(127)
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
			if err := fn(ctx, inv); err != nil {
				// The single seam between a builtin's int exit code and the
				// shell backend's representation. interp.ExitStatus is a uint8,
				// so the code is reduced modulo 256 here — matching how the OS
				// reports process exit statuses, and in exactly one place.
				var ee *command.ExitError
				if errors.As(err, &ee) {
					return interp.ExitStatus(uint8(ee.Code))
				}
				// Defensive: a builtin may surface the backend's native exit
				// status directly. It is already a uint8, so pass it through.
				var exitStatus interp.ExitStatus
				if errors.As(err, &exitStatus) {
					return exitStatus
				}
				fmt.Fprintf(hc.Stderr, "%s: %v\n", args[0], err)
				return interp.ExitStatus(1)
			}
			return nil
		}
	}
}
