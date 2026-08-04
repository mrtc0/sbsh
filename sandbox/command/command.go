// Package command is what a command running inside the sandbox is given, and
// how it reports back.
//
// The builtins are written against these types, and a host adding a command of
// its own in Go writes against the same ones — a command from outside is not a
// second kind of command with a shape of its own.
//
// Nothing here refers to the shell that does the dispatching. A command
// receives its arguments, its streams, the environment, the working directory
// and the sandbox's filesystem, and reports back with [Exit] or [Exitf].
package command

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"

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
//
// Run returns [Exit] or [Exitf] — that is the whole return contract, for
// success as much as for failure. See [Exit].
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
//
// It returns [Exit] or [Exitf], whether it succeeded or not. See [Exit].
type RunFunc func(ctx context.Context, inv *Invocation) error

// ExitError is how a command reports its exit status, along with the message to
// show for it when there is one. It is what [Exit] and [Exitf] return, and a
// command has no reason to build one by hand.
//
// Code carries an int so a caller can pass whatever its underlying library
// produced without worrying about truncation; the sandbox reduces it modulo 256
// in one place, the way the OS reports a process exit status.
//
// Msg is what the sandbox prints to stderr, prefixed with the command's name. It
// is empty when the command has nothing to say — a status of its own, like
// grep's 1 for "no match", is not a diagnostic.
//
// Returning any other error is outside the contract: the sandbox has no status
// to go by, so it falls back to printing the error and exiting 1.
type ExitError struct {
	Code int
	Msg  string
}

// Error renders the message when there is one, so a Go caller inspecting the
// error reads what the caller of the command would have been shown, and the
// status otherwise.
func (e *ExitError) Error() string {
	if e.Msg != "" {
		return e.Msg
	}
	return fmt.Sprintf("exit status %d", uint8(e.Code))
}

// Exit returns the error a command returns to finish. It is the normal return
// path and not only the failing one: the command picks its status, and attaches
// a message only when the caller should see one.
//
//	return command.Exit(0)              // done, nothing to say
//	return command.Exit(1)              // grep's "no match": a status, not an error
//	return command.Exit(2, "bad usage") // prints "name: bad usage" to stderr
//
// The msg parts are joined with a space, so the pieces of a sentence may be
// passed separately. Passing none prints nothing.
//
// Do not wrap the result: the sandbox shows the message the [ExitError] carries,
// so text wrapped around it is dropped. Build the message with [Exitf] instead.
func Exit(code int, msg ...string) error {
	return &ExitError{Code: code, Msg: strings.Join(msg, " ")}
}

// Exitf is [Exit] with the message formatted, which is how a command reports
// what went wrong together with the status for it:
//
//	return command.Exitf(1, "cannot read %s: %v", name, err)
func Exitf(code int, format string, args ...any) error {
	return &ExitError{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// New returns a [Command] with the given metadata that runs fn. It saves
// declaring a type for a command that holds no state:
//
//	sandbox.WithCommand(command.New("hello", "greet the caller",
//		func(_ context.Context, inv *command.Invocation) error {
//			if _, err := fmt.Fprintln(inv.Stdout, "hello"); err != nil {
//				return command.Exitf(1, "%v", err)
//			}
//			return command.Exit(0)
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
