package command_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrtc0/sbsh/sandbox/command"
)

func TestInvocation_Abs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		dir  string
		path string
		want string
	}{
		{name: "relative path resolves against the working directory", dir: "/work", path: "a.txt", want: "/work/a.txt"},
		{name: "absolute path is kept", dir: "/work", path: "/tmp/a.txt", want: "/tmp/a.txt"},
		{name: "dot segments are normalized away", dir: "/work/sub", path: "../a.txt", want: "/work/a.txt"},
		{name: "an empty working directory is the root", dir: "", path: "a.txt", want: "/a.txt"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			inv := &command.Invocation{Dir: tc.dir}
			assert.Equal(t, tc.want, inv.Abs(tc.path))
		})
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	cmd := command.New("hello", "greet the caller", func(_ context.Context, inv *command.Invocation) error {
		_, err := fmt.Fprintln(inv.Stdout, "hello", inv.Args[0])
		return err
	})

	assert.Equal(t, "hello", cmd.Name())
	assert.Equal(t, "greet the caller", cmd.Description())

	var out bytes.Buffer
	require.NoError(t, cmd.Run(context.Background(), &command.Invocation{Args: []string{"world"}, Stdout: &out}))
	assert.Equal(t, "hello world\n", out.String())
}

func TestNew_withoutImplementation(t *testing.T) {
	t.Parallel()

	err := command.New("hello", "greet the caller", nil).Run(context.Background(), &command.Invocation{})
	assert.ErrorContains(t, err, "no implementation")
}

func TestInvocation_Getenv(t *testing.T) {
	t.Parallel()

	inv := &command.Invocation{Env: testEnviron{"HOME": "/home/user", "EMPTY": ""}}
	assert.Equal(t, "/home/user", inv.Getenv("HOME"))
	assert.Equal(t, "", inv.Getenv("EMPTY"))
	assert.Equal(t, "", inv.Getenv("MISSING"))

	// A host driving Run directly need not wire an environment up.
	assert.Equal(t, "", (&command.Invocation{}).Getenv("HOME"))
}

// testEnviron is the environment a host would stub in to drive Run itself.
type testEnviron map[string]string

func (e testEnviron) Lookup(name string) (string, bool) {
	v, ok := e[name]
	return v, ok
}

func (e testEnviron) All() []string {
	out := make([]string, 0, len(e))
	for k, v := range e {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

func TestExit(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		err     error
		code    int
		msg     string
		wantErr string
	}{
		{
			name:    "a status on its own carries no message to print",
			err:     command.Exit(1),
			code:    1,
			msg:     "",
			wantErr: "exit status 1",
		},
		{
			name:    "a message is what the caller of the command is shown",
			err:     command.Exit(2, "bad usage"),
			code:    2,
			msg:     "bad usage",
			wantErr: "bad usage",
		},
		{
			name:    "the parts of a sentence may be passed separately",
			err:     command.Exit(2, "bad", "usage"),
			code:    2,
			msg:     "bad usage",
			wantErr: "bad usage",
		},
		{
			name:    "Exitf formats the message",
			err:     command.Exitf(1, "cannot read %s: %v", "a.txt", errors.New("no such file")),
			code:    1,
			msg:     "cannot read a.txt: no such file",
			wantErr: "cannot read a.txt: no such file",
		},
		{
			name:    "an empty formatted message is no message at all",
			err:     command.Exitf(1, ""),
			code:    1,
			msg:     "",
			wantErr: "exit status 1",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var exitErr *command.ExitError
			require.ErrorAs(t, tc.err, &exitErr)
			assert.Equal(t, tc.code, exitErr.Code)
			assert.Equal(t, tc.msg, exitErr.Msg)
			assert.Equal(t, tc.wantErr, tc.err.Error())
		})
	}
}

// TestExit_wrapped pins that a wrapped status is still found. Wrapping is a
// misuse — the wrapper text is not what the sandbox displays — but the payload
// has to survive it, so the exit code is never lost to it.
func TestExit_wrapped(t *testing.T) {
	t.Parallel()

	err := fmt.Errorf("wrapped: %w", command.Exit(3, "bad usage"))

	var exitErr *command.ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 3, exitErr.Code)
	assert.Equal(t, "bad usage", exitErr.Msg)
	assert.Equal(t, "bad usage", exitErr.Error(), "the ExitError renders its own message, not the wrapper's")
}

// TestExitError_Error pins that the message renders the status the shell will
// report, not the int it was handed: ExitError carries an int so a caller can
// pass through whatever its library produced, and 300 lands as 44.
func TestExitError_Error(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "exit status 44", (&command.ExitError{Code: 300}).Error())
}
