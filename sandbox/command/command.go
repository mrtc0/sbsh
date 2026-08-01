// Package command is the extension point for adding commands to the sandbox
// from Go. A host that needs a command sbsh does not ship implements [Command]
// and registers it with sandbox.WithCommand; from then on the shell dispatches
// it exactly like a builtin.
//
// The interface deliberately says nothing about the shell backend. A command
// receives its arguments, its streams, the sandbox filesystem and the working
// directory, and returns an error — which is all a builtin gets too.
package command

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path"

	"github.com/mrtc0/sbsh/vfs"
)

// Command is the minimum a custom command must implement: a name to be invoked
// by, a one-line description for the host to display, and the behaviour itself.
//
// Name must be a plain word — letters, digits, "_", "." and "-" — because it is
// matched against the first word of a command, not looked up on a PATH.
// Registration rejects anything else, an empty Description, a name a builtin
// already has, and a name the shell handles itself (such as "cd" or "echo"),
// since a command by those names could never be reached.
type Command interface {
	Name() string
	Description() string
	Run(ctx context.Context, inv *Invocation) error
}

// Invocation is everything a command is given for one call. It is the public
// counterpart of what the builtins receive, with no reference to the shell
// interpreter in it.
//
// Nothing in it is valid after Run returns: the streams belong to the shell,
// which may be piping them into the next command of a pipeline.
type Invocation struct {
	// Name is the command's own name, as it was invoked. A command that writes a
	// diagnostic of its own prefixes it with this, so its output reads the same
	// as the error the sandbox reports on its behalf.
	Name string

	// Args are the arguments the command was called with, without the name.
	Args []string

	// Dir is the shell's current working directory, as an absolute path in the
	// sandbox filesystem. Use Abs to resolve an argument against it.
	Dir string

	// Stdin, Stdout and Stderr are the command's standard streams. They are
	// whatever the shell wired up: a pipe, a redirected file in the sandbox
	// filesystem, or the sandbox's captured output.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	// FS is the sandbox filesystem, with the mounts resolved and the deny
	// patterns in force. It implements afero.Fs, so afero's helpers apply. It is
	// the only filesystem a command may touch: reaching for os directly steps
	// outside the sandbox and is a bug in the command.
	FS vfs.FS

	// HTTP reaches the network within the limits of the sandbox's network
	// policy. It is nil when no policy was configured, and a command that finds
	// it nil has no other way out: there is no unrestricted client to fall back
	// on.
	HTTP *http.Client
}

// Abs resolves p against the working directory and normalizes it, giving the
// absolute sandbox path to hand to FS.
func (inv *Invocation) Abs(p string) string {
	if !path.IsAbs(p) {
		p = path.Join(inv.Dir, p)
	}
	return vfs.Normalize(p)
}

// ExitError is how a command reports a non-zero exit status. Returning any
// other error makes the sandbox print it, prefixed with the command's name, and
// exit 1 — which is what a command wants for a usage or I/O failure, while a
// command with statuses of its own (like grep's 1 for "no match") returns this
// instead.
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("exit status %d", uint8(e.Code)) }

// Exit returns an error that exits the command with code. As with a process,
// the status the shell sees is the code modulo 256.
func Exit(code int) error { return &ExitError{Code: code} }

// RunFunc is the behaviour half of a Command.
type RunFunc func(ctx context.Context, inv *Invocation) error

// New returns a Command with the given metadata that runs fn. It saves
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
