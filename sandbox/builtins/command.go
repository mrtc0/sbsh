package builtins

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"

	"github.com/mrtc0/sh/v3/expand"
	"github.com/mrtc0/sh/v3/interp"

	"github.com/mrtc0/sbsh/sandbox/command"
	"github.com/mrtc0/sbsh/sandbox/python"
	"github.com/mrtc0/sbsh/vfs"
)

// Env is the environment passed to a command implementation.
type Env struct {
	// Name is the command's own name, as it was invoked. A command that writes a
	// warning of its own prefixes it with this, so its output reads the same as
	// the error ExecMiddleware reports on its behalf.
	Name string

	// FS is the sandbox's mount-resolved filesystem. It implements afero.Fs.
	FS vfs.FS
	// HC is the handler context from mvdan/sh interp,
	// which provides access to stdin, stdout, stderr, and the current working directory.
	HC interp.HandlerContext

	// HTTP reaches the network within the limits of the sandbox's network
	// policy. It is nil when no policy was configured, and a command that finds
	// it nil has no other way out: there is no unrestricted client to fall back
	// on.
	HTTP *http.Client

	// Python is the interpreter for running Python code. It is always available in the sandbox.
	Python python.Interpreter
}

func (e *Env) Abs(p string) string {
	if !path.IsAbs(p) {
		p = path.Join(e.HC.Dir, p)
	}
	return vfs.Normalize(p)
}

// Func is the type of a command implementation. It takes a context, an environment, and a slice of arguments.
type Func func(ctx context.Context, env *Env, args []string) error

// exitError is how a builtin reports a non-zero exit code. Builtins return it
// instead of interp.ExitStatus so they don't depend on the shell backend:
// exitCode is the single seam that translates the code into the backend's
// representation. Because it carries an int, callers pass whatever their
// underlying library produces without worrying about truncation here.
//
// It is the internal twin of [command.ExitError], which is what a custom
// command returns; exitCode accepts either.
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", uint8(e.code)) }

// exit reports a non-zero exit code from a builtin.
func exit(code int) error { return exitError{code} }

type Options struct {
	HTTP   *http.Client
	Python python.Interpreter
	// Commands are the custom commands the host registered, keyed by name. They
	// take the same dispatch path as the builtins below — same lookup, same
	// exit-code translation, same "command not found" — and the sandbox keeps
	// the two sets of names disjoint, so which is consulted first is not
	// observable.
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

// runner is one command's implementation, reduced to the shape dispatch needs.
// Both a builtin and a custom command become one of these, so everything after
// the lookup is common to them.
type runner func(ctx context.Context, hc interp.HandlerContext, args []string) error

// resolve finds the implementation of name among the builtins and the host's
// custom commands.
func resolve(name string, fsys vfs.FS, opts Options) (runner, bool) {
	if fn, ok := registry[name]; ok {
		return func(ctx context.Context, hc interp.HandlerContext, args []string) error {
			env := &Env{Name: name, FS: fsys, HC: hc, HTTP: opts.HTTP, Python: opts.Python}
			return fn(ctx, env, args)
		}, true
	}
	if cmd, ok := opts.Commands[name]; ok {
		return func(ctx context.Context, hc interp.HandlerContext, args []string) error {
			return cmd.Run(ctx, &command.Invocation{
				Name:   name,
				Args:   args,
				Dir:    hc.Dir,
				Stdin:  hc.Stdin,
				Stdout: hc.Stdout,
				Stderr: hc.Stderr,
				Env:    lookupEnv(hc.Env),
				FS:     fsys,
				HTTP:   opts.HTTP,
			})
		}, true
	}
	return nil, false
}

// lookupEnv adapts the shell's environment to the lookup a [command.Invocation]
// carries. Only plain string variables are reported: an array has no single
// value to hand back, so it reads as unset.
func lookupEnv(env expand.Environ) func(string) (string, bool) {
	return func(name string) (string, bool) {
		vr := env.Get(name)
		if !vr.IsSet() || vr.Kind != expand.String {
			return "", false
		}
		return vr.Str, true
	}
}

// exitCode reports the exit status a command asked for, if it asked for one.
//
// This is the single seam between a command's int exit code and the shell
// backend's representation. interp.ExitStatus is a uint8, so a caller reduces
// the code modulo 256 — matching how the OS reports process exit statuses, and
// in exactly one place.
func exitCode(err error) (int, bool) {
	var builtinErr exitError
	if errors.As(err, &builtinErr) {
		return builtinErr.code, true
	}
	var customErr *command.ExitError
	if errors.As(err, &customErr) {
		return customErr.Code, true
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

			run, ok := resolve(args[0], fsys, opts)
			if !ok {
				fmt.Fprintf(hc.Stderr, "%s: command not found\n", args[0])
				return interp.ExitStatus(127)
			}

			if err := run(ctx, hc, args[1:]); err != nil {
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
