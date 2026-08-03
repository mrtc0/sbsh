package builtins

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/afero"

	"github.com/mrtc0/sbsh/sandbox/python"

	"github.com/mrtc0/sbsh/sandbox/command"
)

// pythonCommand is the implementation of the "python" command in the sandbox.
// It supports running Python code from a string or a file,
// and it sets up the environment for the Python interpreter.
func pythonCommand(ctx context.Context, inv *command.Invocation) error {
	var code string
	var argv []string
	switch {
	case len(inv.Args) >= 2 && inv.Args[0] == "-c":
		code = inv.Args[1]
		argv = append([]string{"-c"}, inv.Args[2:]...)
	case len(inv.Args) >= 1 && !strings.HasPrefix(inv.Args[0], "-"):
		data, err := afero.ReadFile(inv.FS, inv.Abs(inv.Args[0]))
		if err != nil {
			return err
		}
		code = string(data)
		argv = inv.Args
	default:
		return fmt.Errorf("usage: python -c CODE [args...] | python FILE [args...]")
	}

	res, runErr := inv.Python.Run(ctx, python.Invocation{
		Code:  code,
		Argv:  argv,
		Cwd:   inv.Dir,
		Env:   inv.Env.All(),
		Stdin: inv.Stdin,
		FS:    inv.FS,
	})

	// Flush whatever the interpreter captured before acting on runErr: a
	// timeout or cancellation still produces partial output that the user
	// expects to see. Write errors (e.g. the output limit is hit) must not be
	// swallowed either, or truncation would be reported as success.
	if _, err := io.WriteString(inv.Stdout, res.Stdout); err != nil {
		return err
	}
	if _, err := io.WriteString(inv.Stderr, res.Stderr); err != nil {
		return err
	}

	if runErr != nil {
		return runErr
	}
	if !res.Ok() {
		return exit(int(res.ExitCode))
	}
	return nil
}

func init() {
	Register("python", pythonCommand)
	Register("python3", pythonCommand)
}
