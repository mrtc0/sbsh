package command_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrtc0/sbsh/sandbox/command"
	"github.com/mrtc0/sbsh/sandbox/exec"
)

// fakeExecutor records the request it was handed and returns what the test set
// up, standing in for the runtime the surface is defined ahead of.
type fakeExecutor struct {
	got    command.NestedRequest
	calls  int
	result *exec.Result
	err    error
}

func (e *fakeExecutor) Run(_ context.Context, req command.NestedRequest) (*exec.Result, error) {
	e.calls++
	e.got = req
	return e.result, e.err
}

func TestRunNestedPassesTheRequestThrough(t *testing.T) {
	t.Parallel()

	exe := &fakeExecutor{result: &exec.Result{Stdout: "a\nb\n"}}
	inv := &command.Invocation{Name: "orchestrate", Dir: "/work", Nested: exe}

	req := command.NestedRequest{
		Script:      "ls | sort",
		Dir:         "sub",
		Env:         []string{"LANG=C"},
		Timeout:     time.Second,
		OutputLimit: 1024,
	}
	res, err := inv.RunNested(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, 1, exe.calls)
	assert.Equal(t, req, exe.got)
	assert.Equal(t, "a\nb\n", res.Stdout)
	assert.True(t, res.OK())
}

func TestRunNestedReportsWhatTheChildReported(t *testing.T) {
	t.Parallel()

	// A child that failed on its own is not an error here, the same way it is
	// not one for a host calling the sandbox.
	exe := &fakeExecutor{result: &exec.Result{ExitCode: 1, Outcome: exec.OutcomeCompleted}}
	inv := &command.Invocation{Name: "orchestrate", Nested: exe}

	res, err := inv.RunNested(context.Background(), command.NestedRequest{Script: "false"})

	require.NoError(t, err)
	assert.False(t, res.OK())
	assert.Equal(t, 1, res.ExitCode)
	assert.Equal(t, exec.OutcomeCompleted, res.Outcome)
}

func TestRunNestedRejectsARequestWithNoScript(t *testing.T) {
	t.Parallel()

	for name, script := range map[string]string{
		"empty":      "",
		"whitespace": " \n\t",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			exe := &fakeExecutor{result: &exec.Result{}}
			inv := &command.Invocation{Name: "orchestrate", Nested: exe}

			res, err := inv.RunNested(context.Background(), command.NestedRequest{Script: script})

			require.Error(t, err)
			assert.Contains(t, err.Error(), "orchestrate")
			assert.Equal(t, exec.OutcomeInvalid, res.Outcome)
			assert.Equal(t, 2, res.ExitCode)
			assert.Zero(t, exe.calls, "an invalid request never reaches the executor")
		})
	}
}

func TestRunNestedReportsASandboxThatOffersNoNestedExecution(t *testing.T) {
	t.Parallel()

	inv := &command.Invocation{Name: "orchestrate"}

	res, err := inv.RunNested(context.Background(), command.NestedRequest{Script: "echo hi"})

	// There is nothing here to refuse the request: the runtime has no nested
	// execution at all, which is what the outcome says.
	require.NoError(t, err)
	assert.Equal(t, exec.OutcomeUnsupported, res.Outcome)
	assert.Equal(t, 126, res.ExitCode)
	assert.Contains(t, res.Stderr, "nested execution is not available")
}

func TestRunNestedSurfacesAnExecutorFailure(t *testing.T) {
	t.Parallel()

	// The failure of the sandbox itself is the other half of the contract: the
	// result is still populated, so a command can read it without unwrapping.
	boom := errors.New("boom")
	res, execErr := exec.Internal(boom)
	inv := &command.Invocation{Name: "orchestrate", Nested: &fakeExecutor{result: res, err: execErr}}

	got, err := inv.RunNested(context.Background(), command.NestedRequest{Script: "echo hi"})

	require.ErrorIs(t, err, boom)
	assert.Equal(t, exec.OutcomeInternal, got.Outcome)
	assert.Equal(t, 125, got.ExitCode)
}
