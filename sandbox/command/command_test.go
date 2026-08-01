package command_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrtc0/sbsh/sandbox/command"
)

func TestNew(t *testing.T) {
	t.Parallel()

	cmd := command.New("hello", "greet the caller", func(_ context.Context, inv *command.Invocation) error {
		_, err := fmt.Fprintln(inv.Stdout, "hello", inv.Args[0])
		return err
	})

	assert.Equal(t, "hello", cmd.Name())
	assert.Equal(t, "greet the caller", cmd.Description())

	var out testWriter
	require.NoError(t, cmd.Run(context.Background(), &command.Invocation{Args: []string{"world"}, Stdout: &out}))
	assert.Equal(t, "hello world\n", out.String())
}

func TestNew_withoutImplementation(t *testing.T) {
	t.Parallel()

	err := command.New("hello", "greet the caller", nil).Run(context.Background(), &command.Invocation{})
	assert.Error(t, err)
}

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

func TestInvocation_Getenv(t *testing.T) {
	t.Parallel()

	set := map[string]string{"HOME": "/home/user", "EMPTY": ""}
	inv := &command.Invocation{Env: func(name string) (string, bool) {
		v, ok := set[name]
		return v, ok
	}}
	assert.Equal(t, "/home/user", inv.Getenv("HOME"))
	assert.Equal(t, "", inv.Getenv("EMPTY"))
	assert.Equal(t, "", inv.Getenv("MISSING"))

	// A host driving Run directly need not wire an environment up.
	assert.Equal(t, "", (&command.Invocation{}).Getenv("HOME"))
}

func TestExit(t *testing.T) {
	t.Parallel()

	err := fmt.Errorf("wrapped: %w", command.Exit(3))

	var exitErr *command.ExitError
	require.True(t, errors.As(err, &exitErr))
	assert.Equal(t, 3, exitErr.Code)
	assert.Equal(t, "exit status 3", exitErr.Error())
}

type testWriter struct{ b []byte }

func (w *testWriter) Write(p []byte) (int, error) {
	w.b = append(w.b, p...)
	return len(p), nil
}

func (w *testWriter) String() string { return string(w.b) }
