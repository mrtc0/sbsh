package sandbox

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrtc0/sbsh/sandbox/command"
)

// TestSandbox_CustomCommand covers what a registered command has to be able to
// do to count as a command: take arguments, read the standard input it was
// given, reach the sandbox filesystem, appear in a pipeline, and decide the
// script's exit status.
func TestSandbox_CustomCommand(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		cmds       []command.Command
		script     string
		stdin      io.Reader
		wantStdout string
		wantStderr string
		wantExit   int
	}{
		{
			name:       "a registered command runs with its arguments",
			cmds:       []command.Command{echoArgs()},
			script:     "args a b c",
			wantStdout: "a|b|c\n",
		},
		{
			name:       "a registered command reads the script's standard input",
			cmds:       []command.Command{upper()},
			script:     "upper",
			stdin:      strings.NewReader("hello"),
			wantStdout: "HELLO",
		},
		{
			name:       "a registered command takes part in a pipeline",
			cmds:       []command.Command{upper()},
			script:     "echo hello | upper | tee /tmp/out.txt | wc -c",
			wantStdout: "       6\n",
		},
		{
			name:       "a registered command reads a file through the sandbox filesystem",
			cmds:       []command.Command{readFile()},
			script:     "cd /tmp && echo content > f.txt && readfile f.txt",
			wantStdout: "content\n",
		},
		{
			name:       "a registered command reads the environment the script sees",
			cmds:       []command.Command{showEnv()},
			script:     "export EXPORTED=1; LANG=C showenv EXPORTED LANG NOPE",
			wantStdout: "EXPORTED=1|LANG=C|NOPE=<unset>\n",
		},
		{
			name:     "a registered command decides the exit status",
			cmds:     []command.Command{failing(3)},
			script:   "fail",
			wantExit: 3,
		},
		{
			name:       "a plain error is reported with the command's name and exits 1",
			cmds:       []command.Command{broken()},
			script:     "broken",
			wantStderr: "broken: something went wrong\n",
			wantExit:   1,
		},
		{
			name:       "a command registered on another sandbox is not found",
			script:     "args a",
			wantStderr: "args: command not found",
			wantExit:   127,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sb, err := New(context.Background(), WithCommand(tc.cmds...))
			require.NoError(t, err)
			t.Cleanup(func() { _ = sb.Close() })

			res, err := sb.Exec(context.Background(), tc.script, tc.stdin)
			require.NoError(t, err)

			assert.Equal(t, tc.wantExit, res.ExitCode, "exit code")
			assert.Equal(t, tc.wantStdout, res.Stdout, "stdout")
			if tc.wantStderr != "" {
				assert.Contains(t, res.Stderr, tc.wantStderr, "stderr")
			}
		})
	}
}

// TestSandbox_CustomCommandSeesTheDenyPatterns pins that a custom command is
// inside the sandbox rather than beside it: it is handed the same filesystem
// handle the builtins get, with the deny patterns already layered on.
func TestSandbox_CustomCommandSeesTheDenyPatterns(t *testing.T) {
	t.Parallel()

	sb, err := New(context.Background(),
		WithCommand(readFile()),
		WithDenyPaths("**/.env"),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sb.Close() })

	require.NoError(t, afero.WriteFile(sb.router, "/tmp/.env", []byte("SECRET=1"), 0o600))

	res, err := sb.Exec(context.Background(), "readfile /tmp/.env", nil)
	require.NoError(t, err)
	assert.Equal(t, 1, res.ExitCode)
	assert.Contains(t, res.Stderr, "permission denied")
	assert.Empty(t, res.Stdout)
}

func TestSandbox_Commands(t *testing.T) {
	t.Parallel()

	sb, err := New(context.Background(), WithCommand(upper()), WithCommand(echoArgs()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sb.Close() })

	names := make([]string, 0, 2)
	for _, cmd := range sb.Commands() {
		names = append(names, cmd.Name()+": "+cmd.Description())
	}
	assert.Equal(t, []string{"args: join the arguments", "upper: upper-case the standard input"}, names)
}

func TestNew_RejectsInvalidCommand(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		cmds    []command.Command
		wantErr string
	}{
		{
			name:    "nil command",
			cmds:    []command.Command{nil},
			wantErr: "command is nil",
		},
		{
			name:    "empty name",
			cmds:    []command.Command{command.New("", "does nothing", nil)},
			wantErr: "name is empty",
		},
		{
			name:    "name that is not a plain word",
			cmds:    []command.Command{command.New("my cmd", "does nothing", nil)},
			wantErr: "name must match",
		},
		{
			name:    "path-like name",
			cmds:    []command.Command{command.New("/bin/tool", "does nothing", nil)},
			wantErr: "name must match",
		},
		{
			name:    "empty description",
			cmds:    []command.Command{command.New("tool", "  ", nil)},
			wantErr: "description is empty",
		},
		{
			name:    "name of a builtin",
			cmds:    []command.Command{command.New("grep", "a better grep", nil)},
			wantErr: "a builtin already has that name",
		},
		{
			name:    "name the shell handles itself",
			cmds:    []command.Command{command.New("cd", "a better cd", nil)},
			wantErr: "the shell handles that name itself",
		},
		{
			// "time" is the name that would otherwise register and then be
			// swallowed silently: the shell measures what follows it.
			name:    "name of a keyword",
			cmds:    []command.Command{command.New("time", "a better time", nil)},
			wantErr: "the shell handles that name itself",
		},
		{
			name:    "name of a keyword that breaks parsing",
			cmds:    []command.Command{command.New("if", "a better if", nil)},
			wantErr: "the shell handles that name itself",
		},
		{
			name:    "duplicate name",
			cmds:    []command.Command{upper(), command.New("upper", "another one", nil)},
			wantErr: "already registered",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sb, err := New(context.Background(), WithCommand(tc.cmds...))
			if sb != nil {
				t.Cleanup(func() { _ = sb.Close() })
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func echoArgs() command.Command {
	return command.New("args", "join the arguments", func(_ context.Context, inv *command.Invocation) error {
		_, err := fmt.Fprintln(inv.Stdout, strings.Join(inv.Args, "|"))
		return err
	})
}

func upper() command.Command {
	return command.New("upper", "upper-case the standard input", func(_ context.Context, inv *command.Invocation) error {
		b, err := io.ReadAll(inv.Stdin)
		if err != nil {
			return err
		}
		_, err = io.WriteString(inv.Stdout, strings.ToUpper(string(b)))
		return err
	})
}

func showEnv() command.Command {
	return command.New("showenv", "print the named environment variables", func(_ context.Context, inv *command.Invocation) error {
		parts := make([]string, 0, len(inv.Args))
		for _, name := range inv.Args {
			value, ok := inv.Env.Lookup(name)
			if !ok {
				value = "<unset>"
			}
			parts = append(parts, name+"="+value)
		}
		_, err := fmt.Fprintln(inv.Stdout, strings.Join(parts, "|"))
		return err
	})
}

func readFile() command.Command {
	return command.New("readfile", "print a file from the sandbox filesystem", func(_ context.Context, inv *command.Invocation) error {
		b, err := afero.ReadFile(inv.FS, inv.Abs(inv.Args[0]))
		if err != nil {
			return err
		}
		_, err = inv.Stdout.Write(b)
		return err
	})
}

func failing(code int) command.Command {
	return command.New("fail", "exit with a fixed status", func(_ context.Context, _ *command.Invocation) error {
		return command.Exit(code)
	})
}

func broken() command.Command {
	return command.New("broken", "fail with a plain error", func(_ context.Context, _ *command.Invocation) error {
		return fmt.Errorf("something went wrong")
	})
}
