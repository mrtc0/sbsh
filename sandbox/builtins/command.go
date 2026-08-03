package builtins

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"

	"github.com/mrtc0/sh/v3/expand"
	"github.com/mrtc0/sh/v3/interp"

	"github.com/mrtc0/sbsh/sandbox/python"
	"github.com/mrtc0/sbsh/vfs"
)

// Env is the environment passed to a command implementation.
type Env struct {
	// Name is the command's own name, as it was invoked. A command that writes a
	// warning of its own prefixes it with this, so its output reads the same as
	// the error ExecMiddleware reports on its behalf.
	Name string

	// Dir is the shell's current working directory, as an absolute path in the
	// sandbox filesystem. Abs resolves an argument against it.
	Dir string

	// Stdin, Stdout and Stderr are the command's standard streams, as the shell
	// wired them up: a pipe, a redirected file in the sandbox filesystem, or the
	// sandbox's captured output.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	// FS is the sandbox's mount-resolved filesystem. It implements afero.Fs.
	FS vfs.FS

	// HTTP reaches the network within the limits of the sandbox's network
	// policy. It is nil when no policy was configured, and a command that finds
	// it nil has no other way out: there is no unrestricted client to fall back
	// on.
	HTTP *http.Client

	// Python is the interpreter for running Python code. It is always available in the sandbox.
	Python python.Interpreter

	// Env is the shell's variables. It is nil only when a caller builds an Env
	// by hand and leaves it out; ExecMiddleware always populates it.
	Env Environ
}

// Environ is read access to the shell's variables. It is an interface rather
// than a lookup function because a command may need the whole environment and
// not just one name: python hands the interpreter every variable it can see.
type Environ interface {
	// Lookup returns the value of name and whether it is set at all, so an unset
	// variable is distinguishable from one set to the empty string.
	Lookup(name string) (value string, ok bool)

	// All returns the environment as "NAME=value" pairs. Only plain string
	// variables appear: an array has no single value to render.
	All() []string
}

// shellEnviron adapts the shell's environment to Environ.
type shellEnviron struct{ env expand.Environ }

// NewEnviron returns an Environ backed by the shell's variables.
func NewEnviron(env expand.Environ) Environ { return shellEnviron{env} }

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

func (e *Env) Abs(p string) string {
	if !path.IsAbs(p) {
		p = path.Join(e.Dir, p)
	}
	return vfs.Normalize(p)
}

// Func is the type of a command implementation. It takes a context, an environment, and a slice of arguments.
type Func func(ctx context.Context, env *Env, args []string) error

// exitError is how a builtin reports a non-zero exit code. Builtins return it
// instead of interp.ExitStatus so they don't depend on the shell backend:
// ExecMiddleware is the single seam that translates the code into the backend's
// representation. Because it carries an int, callers pass whatever their
// underlying library produces without worrying about truncation here.
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", uint8(e.code)) }

// exit reports a non-zero exit code from a builtin.
func exit(code int) error { return exitError{code} }

type Options struct {
	HTTP   *http.Client
	Python python.Interpreter
}

var registry = map[string]Func{}

func Register(name string, fn Func) { registry[name] = fn }

// ExecMiddleware returns an interp.ExecHandlerFunc that looks up the command in the registry and executes it.
func ExecMiddleware(fsys vfs.FS, opts Options) func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			hc := interp.HandlerCtx(ctx)
			if len(args) == 0 {
				return interp.ExitStatus(0)
			}

			fn, ok := registry[args[0]]
			if !ok {
				fmt.Fprintf(hc.Stderr, "%s: command not found\n", args[0])
				return interp.ExitStatus(127)
			}

			// The one place the shell's handler context is unpacked: the payload a
			// command receives carries no type from the shell backend.
			env := &Env{
				Name:   args[0],
				Dir:    hc.Dir,
				Stdin:  hc.Stdin,
				Stdout: hc.Stdout,
				Stderr: hc.Stderr,
				FS:     fsys,
				HTTP:   opts.HTTP,
				Python: opts.Python,
				Env:    NewEnviron(hc.Env),
			}
			if err := fn(ctx, env, args[1:]); err != nil {
				// The single seam between a builtin's int exit code and the
				// shell backend's representation. interp.ExitStatus is a uint8,
				// so the code is reduced modulo 256 here — matching how the OS
				// reports process exit statuses, and in exactly one place.
				var ee exitError
				if errors.As(err, &ee) {
					return interp.ExitStatus(uint8(ee.code))
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
