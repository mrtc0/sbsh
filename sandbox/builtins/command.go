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

// Invocation is what a builtin receives for one call. It is the very type a
// custom command registered from Go receives: builtins are not a separate kind of
// command, so there is one payload to build and one signature to implement.
type Invocation = command.Invocation

// Func is the type of a command implementation, builtin or custom.
type Func = command.RunFunc

// exit reports a non-zero exit code from a builtin. It is [command.Exit]: a
// builtin and a custom command report a status the same way, so exitCode has one
// representation to translate rather than two.
func exit(code int) error { return command.Exit(code) }

type Options struct {
	HTTP   *http.Client
	Python python.Interpreter
	// Commands are the custom commands the host registered, keyed by name. The
	// lookup in resolve is the only thing that tells them from a builtin, and the
	// sandbox keeps the two sets of names disjoint, so which one is consulted
	// first is not observable.
	Commands map[string]command.Command
}

var registry = map[string]Func{}

func Register(name string, fn Func) { registry[name] = fn }

// Registered reports whether name is a builtin. The sandbox uses it to refuse a
// custom command that would shadow one.
func Registered(name string) bool {
	_, ok := registry[name]
	return ok
}

// resolve finds the implementation of name among the builtins and the host's
// custom commands. Both are a [command.RunFunc], so the lookup is the only place
// the two differ: everything after it — building the invocation, translating the
// exit code, reporting "command not found" — is written once.
func resolve(name string, opts Options) (command.RunFunc, bool) {
	if fn, ok := registry[name]; ok {
		return fn, true
	}
	if cmd, ok := opts.Commands[name]; ok {
		return cmd.Run, true
	}
	return nil, false
}

// invocation is the single place a command's payload is built, for a builtin and
// a custom command alike.
func invocation(name string, args []string, hc interp.HandlerContext, fsys vfs.FS, opts Options) *command.Invocation {
	return &command.Invocation{
		Name:   name,
		Args:   args,
		Dir:    hc.Dir,
		Stdin:  hc.Stdin,
		Stdout: hc.Stdout,
		Stderr: hc.Stderr,
		Env:    shellEnviron{hc.Env},
		FS:     fsys,
		HTTP:   opts.HTTP,
		Python: opts.Python,
	}
}

// shellEnviron adapts the shell's variables to [command.Environ], so what a
// command sees of the environment does not depend on the shell backend. Only
// plain string variables are reported: an array has no single value to hand back,
// so it reads as unset.
type shellEnviron struct{ env expand.Environ }

func (e shellEnviron) Lookup(name string) (string, bool) {
	vr := e.env.Get(name)
	if !vr.IsSet() || vr.Kind != expand.String {
		return "", false
	}
	return vr.Str, true
}

func (e shellEnviron) All() []string {
	var out []string
	e.env.Each(func(name string, vr expand.Variable) bool {
		if vr.IsSet() && vr.Kind == expand.String {
			out = append(out, name+"="+vr.Str)
		}
		return true
	})
	return out
}

// exitCode reports the exit status a command asked for, if it asked for one.
//
// This is the single seam between a command's int exit code and the shell
// backend's representation. interp.ExitStatus is a uint8, so a caller reduces
// the code modulo 256 — matching how the OS reports process exit statuses, and
// in exactly one place.
func exitCode(err error) (int, bool) {
	var exitErr *command.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Code, true
	}
	// Defensive: a command may surface the backend's native exit status
	// directly. It is already a uint8, so it passes through unchanged.
	var exitStatus interp.ExitStatus
	if errors.As(err, &exitStatus) {
		return int(exitStatus), true
	}
	return 0, false
}

// ExecMiddleware returns an interp.ExecHandlerFunc that looks up the command in the registry and executes it.
func ExecMiddleware(fsys vfs.FS, opts Options) func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			hc := interp.HandlerCtx(ctx)
			if len(args) == 0 {
				return interp.ExitStatus(0)
			}

			run, ok := resolve(args[0], opts)
			if !ok {
				fmt.Fprintf(hc.Stderr, "%s: command not found\n", args[0])
				return interp.ExitStatus(127)
			}

			if err := run(ctx, invocation(args[0], args[1:], hc, fsys, opts)); err != nil {
				if code, ok := exitCode(err); ok {
					return interp.ExitStatus(uint8(code))
				}
				fmt.Fprintf(hc.Stderr, "%s: %v\n", args[0], err)
				return interp.ExitStatus(1)
			}
			return nil
		}
	}
}
