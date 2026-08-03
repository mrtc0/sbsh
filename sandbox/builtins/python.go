package builtins

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/afero"

	"github.com/mrtc0/sbsh/sandbox/python"
)

// pythonCommand is the implementation of the "python" command in the sandbox.
// It supports running Python code from a string or a file,
// and it sets up the environment for the Python interpreter.
func pythonCommand(ctx context.Context, env *Env, args []string) error {
	var code string
	var argv []string
	switch {
	case len(args) >= 2 && args[0] == "-c":
		code = args[1]
		argv = append([]string{"-c"}, args[2:]...)
	case len(args) >= 1 && !strings.HasPrefix(args[0], "-"):
		data, err := afero.ReadFile(env.FS, env.Abs(args[0]))
		if err != nil {
			return err
		}
		code = string(data)
		argv = args
	default:
		return fmt.Errorf("usage: python -c CODE [args...] | python FILE [args...]")
	}

	res, runErr := env.Python.Run(ctx, python.Invocation{
		Code:  code,
		Argv:  argv,
		Cwd:   env.HC.Dir,
		Env:   env.Env.All(),
		Stdin: env.HC.Stdin,
		FS:    env.FS,
	})

	// Flush whatever the interpreter captured before acting on runErr: a
	// timeout or cancellation still produces partial output that the user
	// expects to see. Write errors (e.g. the output limit is hit) must not be
	// swallowed either, or truncation would be reported as success.
	if _, err := io.WriteString(env.HC.Stdout, res.Stdout); err != nil {
		return err
	}
	if _, err := io.WriteString(env.HC.Stderr, res.Stderr); err != nil {
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
