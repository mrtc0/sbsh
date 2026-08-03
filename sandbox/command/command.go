// Package command is what a command running inside the sandbox is given, and
// how it reports back.
//
// The builtins are written against these types, and a host adding a command of
// its own in Go writes against the same ones — a command from outside is not a
// second kind of command with a shape of its own.
//
// Nothing here refers to the shell that does the dispatching. A command
// receives its arguments, its streams, the environment, the working directory
// and the sandbox's filesystem, and returns an error.
package command

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path"

	"github.com/mrtc0/sbsh/sandbox/python"
	"github.com/mrtc0/sbsh/vfs"
)

// Command is the minimum a command registered from Go must implement: a name to
// be invoked by, a one-line description for the host to display, and the
// behaviour itself.
//
// Name must be a plain word — letters, digits, "_", "." and "-" — because it is
// matched against the first word of a command line, not looked up on a PATH.
// Registration rejects anything else, an empty Description, and a name that a
// builtin or the shell itself already answers to, since a command by such a name
// could never run.
type Command interface {
	Name() string
	Description() string
	Run(ctx context.Context, inv *Invocation) error
}

// Invocation is everything a command is given for one call.
//
// Nothing in it is valid after the call returns: the streams belong to the
// shell, which may be piping them into the next command of a pipeline.
type Invocation struct {
	// Name is the command's own name, as it was invoked. A command that writes a
	// diagnostic of its own prefixes it with this, so its output reads the same
	// as the error the sandbox reports on its behalf.
	Name string

	// Args are the arguments the command was invoked with, without the name.
	Args []string

	// Dir is the shell's current working directory, as an absolute path in the
	// sandbox filesystem. Abs resolves an argument against it.
	Dir string

	// Stdin, Stdout and Stderr are the command's standard streams, as the shell
	// wired them up: a pipe, a redirected file in the sandbox filesystem, or the
	// sandbox's captured output.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	// FS is the sandbox's mount-resolved filesystem, with the deny patterns in
	// force. It implements afero.Fs. It is the only filesystem a command may
	// touch: reaching for os directly steps outside the sandbox.
	FS vfs.FS

	// HTTP reaches the network within the limits of the sandbox's network
	// policy. It is nil when no policy was configured, and a command that finds
	// it nil has no other way out: there is no unrestricted client to fall back
	// on.
	HTTP *http.Client

	// Python is the interpreter for running Python code. It is always available
	// in the sandbox.
	Python python.Interpreter

	// Env is the shell's variables. It is nil only when a caller builds an
	// Invocation by hand and leaves it out; the sandbox always populates it.
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

// Abs resolves p against the working directory and normalizes it, giving the
// absolute sandbox path to hand to FS.
func (inv *Invocation) Abs(p string) string {
	if !path.IsAbs(p) {
		p = path.Join(inv.Dir, p)
	}
	return vfs.Normalize(p)
}

// Getenv returns the value of the environment variable name, or the empty string
// when it is unset. Use [Environ.Lookup] to tell the two apart.
func (inv *Invocation) Getenv(name string) string {
	if inv.Env == nil {
		return ""
	}
	v, _ := inv.Env.Lookup(name)
	return v
}

// RunFunc is a command's behaviour. Everything the command is given for one
// call is in the Invocation, so a caller needs to hold nothing beside it.
type RunFunc func(ctx context.Context, inv *Invocation) error

// ExitError is how a command reports a non-zero exit status. It carries an int
// so a caller can pass whatever its underlying library produced without
// worrying about truncation; the sandbox reduces it modulo 256 in one place,
// the way the OS reports a process exit status.
//
// Returning any other error makes the sandbox print it, prefixed with the
// command's name, and exit 1 — which is what a usage or I/O failure wants,
// while a command with statuses of its own (like grep's 1 for "no match")
// returns this instead.
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("exit status %d", uint8(e.Code)) }

// Exit returns an error that exits the command with code.
func Exit(code int) error { return &ExitError{Code: code} }

// New returns a [Command] with the given metadata that runs fn. It saves
// declaring a type for a command that holds no state:
//
//	sandbox.WithCommand(command.New("hello", "greet the caller",
//		func(_ context.Context, inv *command.Invocation) error {
//			_, err := fmt.Fprintln(inv.Stdout, "hello")
//			return err
//		}))
func New(name, description string, fn RunFunc) Command {
	return &funcCommand{name: name, description: description, fn: fn}
}

type funcCommand struct {
	name        string
	description string
	fn          RunFunc
}

func (c *funcCommand) Name() string        { return c.name }
func (c *funcCommand) Description() string { return c.description }

func (c *funcCommand) Run(ctx context.Context, inv *Invocation) error {
	if c.fn == nil {
		return fmt.Errorf("command %q has no implementation", c.name)
	}
	return c.fn(ctx, inv)
}
