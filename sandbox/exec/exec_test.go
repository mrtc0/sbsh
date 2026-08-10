package exec_test

import (
	"context"
	"errors"
	"testing"

	"github.com/mrtc0/sh/v3/interp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrtc0/sbsh/sandbox/command"
	"github.com/mrtc0/sbsh/sandbox/exec"
)

// TestFinishClassifiesEveryEnding pins the classification the contract fixes.
// Every entry point ends a run through Finish, so this table is the whole mapping
// from "how the run ended" to what a caller sees, for top-level and nested
// execution alike.
func TestFinishClassifiesEveryEnding(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")

	cases := []struct {
		name string
		// err is what running the request returned.
		err         error
		wantOutcome exec.Outcome
		wantExit    int
		wantErr     error
	}{
		{
			name:        "no error is a completed run that exited zero",
			wantOutcome: exec.OutcomeCompleted,
		},
		{
			name:        "a status from the shell backend is the run's own",
			err:         interp.ExitStatus(3),
			wantOutcome: exec.OutcomeCompleted,
			wantExit:    3,
		},
		{
			// The ending of a nested run of a single command: what it returns is
			// its own ExitError, not a status from the shell backend. Both shapes
			// have to classify the same, or nested execution would report a
			// different result for the same failure.
			name:        "a status a command carries is the run's own",
			err:         command.Exit(3, "no"),
			wantOutcome: exec.OutcomeCompleted,
			wantExit:    3,
		},
		{
			name:        "a status a command carries is reduced modulo 256",
			err:         command.Exit(300),
			wantOutcome: exec.OutcomeCompleted,
			wantExit:    44,
		},
		{
			name:        "a name dispatch could not resolve is a command not found",
			err:         &exec.NotFoundError{Name: "nope"},
			wantOutcome: exec.OutcomeNotFound,
			wantExit:    127,
		},
		{
			// 127 is also a status a request may pick for itself, so it is the error
			// dispatch returns that makes the outcome, not the status it carries.
			name:        "127 a request chose for itself is a completed run",
			err:         interp.ExitStatus(127),
			wantOutcome: exec.OutcomeCompleted,
			wantExit:    127,
		},
		{
			name:        "a deadline that passed is a timeout",
			err:         context.DeadlineExceeded,
			wantOutcome: exec.OutcomeTimedOut,
			wantExit:    137,
		},
		{
			name:        "a cancelled context is a cancellation",
			err:         context.Canceled,
			wantOutcome: exec.OutcomeCanceled,
			wantExit:    130,
		},
		{
			name:        "anything else is a failure of the sandbox",
			err:         boom,
			wantOutcome: exec.OutcomeInternal,
			wantExit:    125,
			wantErr:     boom,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			out := exec.Output{Stdout: "out", Stderr: "err", Truncated: true}
			res, err := exec.Finish(context.Background(), out, tc.err)

			require.NotNil(t, res, "a result is always populated")
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err, "%s is represented in a field, not an error", tc.wantOutcome)
			}
			assert.Equal(t, tc.wantOutcome, res.Outcome, "outcome")
			assert.Equal(t, tc.wantExit, res.ExitCode, "exit code")
			assert.Equal(t, "out", res.Stdout)
			assert.Equal(t, "err", res.Stderr)
			assert.True(t, res.Truncated, "truncation is independent of the outcome")
		})
	}
}

// TestFinishClassifiesAStoppedContext covers the endings where the reason is
// on the context rather than on the error: the run returned something of its own
// after the deadline passed or the caller cancelled.
func TestFinishClassifiesAStoppedContext(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		ctx         func(t *testing.T) context.Context
		wantOutcome exec.Outcome
		wantExit    int
	}{
		{
			name: "a cancelled context reports SIGINT",
			ctx: func(t *testing.T) context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			wantOutcome: exec.OutcomeCanceled,
			wantExit:    130,
		},
		{
			name: "an expired deadline reports SIGKILL",
			ctx: func(t *testing.T) context.Context {
				ctx, cancel := context.WithTimeout(context.Background(), 0)
				t.Cleanup(cancel)
				return ctx
			},
			wantOutcome: exec.OutcomeTimedOut,
			wantExit:    137,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			res, err := exec.Finish(tc.ctx(t), exec.Output{}, errors.New("interrupted"))

			require.NoError(t, err, "a limit the caller asked for is not a failure")
			assert.Equal(t, tc.wantOutcome, res.Outcome)
			assert.Equal(t, tc.wantExit, res.ExitCode)
		})
	}
}

// TestFinishPrefersBeingStoppedOverAStatusTheRunEndedOn pins which of the two
// wins when a run that was stopped still ends on a status. A command that
// watches its context and returns an exit code on the way out is well behaved,
// and taking that code at face value would report the run as having completed:
// the timeout would vanish, and a caller would be left unable to tell a request
// that failed from one it never let finish.
func TestFinishPrefersBeingStoppedOverAStatusTheRunEndedOn(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
	}{
		{"a status from the shell backend", interp.ExitStatus(3)},
		{"a status a command carries", command.Exit(3, "interrupted")},
		{"a name that did not resolve", &exec.NotFoundError{Name: "nope"}},
		// The worst of the lot: a command that exits zero on the way out leaves
		// no error at all, so a run that never finished would read as a success.
		{"nothing at all", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(context.Background(), 0)
			t.Cleanup(cancel)

			res, err := exec.Finish(ctx, exec.Output{}, tc.err)

			require.NoError(t, err, "being stopped is not a failure of the sandbox")
			assert.Equal(t, exec.OutcomeTimedOut, res.Outcome)
			assert.Equal(t, 137, res.ExitCode)
			assert.True(t, res.Stopped())
		})
	}
}

// TestNotFoundErrorIsAStatusToTheShellAndAFactToTheCaller pins the two things
// the error dispatch returns for an unresolved name has to be at once. Being a
// 127 to the shell backend is what lets a script go on past it; being a distinct
// type is what tells that 127 from one a request picked for itself, which is the
// whole reason the fact does not have to be read out of stderr.
func TestNotFoundErrorIsAStatusToTheShellAndAFactToTheCaller(t *testing.T) {
	t.Parallel()

	err := error(&exec.NotFoundError{Name: "nope"})

	var status interp.ExitStatus
	require.ErrorAs(t, err, &status, "the shell backend has to see an exit status")
	assert.Equal(t, interp.ExitStatus(127), status)

	var notFound *exec.NotFoundError
	require.ErrorAs(t, err, &notFound)
	assert.Equal(t, "nope", notFound.Name)
	assert.Equal(t, "nope: command not found", err.Error())
}

// TestResultConstructorsPairTheErrorWithTheOutcome pins the other half of the
// contract: which endings come with a Go error, and which are only a field.
func TestResultConstructorsPairTheErrorWithTheOutcome(t *testing.T) {
	t.Parallel()

	denied := exec.Denied("process substitution is not allowed in the sandbox")
	assert.Equal(t, exec.OutcomeDenied, denied.Outcome)
	assert.Equal(t, 126, denied.ExitCode)
	assert.Equal(t, "sbsh: process substitution is not allowed in the sandbox\n", denied.Stderr,
		"a denial carries no error, so its reason has to be readable on stderr")

	unsupported := exec.Unsupported("nested execution is not available in this sandbox")
	assert.Equal(t, exec.OutcomeUnsupported, unsupported.Outcome)
	assert.Equal(t, 126, unsupported.ExitCode,
		"a shell has no status of its own for it, so it shares the denial's")
	assert.NotEqual(t, exec.OutcomeDenied, unsupported.Outcome,
		"a limitation of the runtime is not a policy denial")
	assert.Equal(t, "sbsh: nested execution is not available in this sandbox\n", unsupported.Stderr)

	boom := errors.New("boom")

	invalid, err := exec.Invalid(boom)
	require.ErrorIs(t, err, boom, "the caller cannot make sense of the request without the detail")
	assert.Equal(t, exec.OutcomeInvalid, invalid.Outcome)
	assert.Equal(t, 2, invalid.ExitCode)

	internal, err := exec.Internal(boom)
	require.ErrorIs(t, err, boom)
	assert.Equal(t, exec.OutcomeInternal, internal.Outcome)
	assert.Equal(t, 125, internal.ExitCode)
}

func TestResultHelpers(t *testing.T) {
	t.Parallel()

	assert.True(t, (&exec.Result{Outcome: exec.OutcomeCompleted}).OK())
	assert.False(t, (&exec.Result{Outcome: exec.OutcomeCompleted, ExitCode: 1}).OK())
	assert.False(t, (&exec.Result{Outcome: exec.OutcomeNotFound, ExitCode: 127}).OK())
	assert.False(t, (&exec.Result{Outcome: exec.OutcomeCompleted, Truncated: true}).Stopped(),
		"truncated output is not a stopped run")
	assert.True(t, (&exec.Result{Outcome: exec.OutcomeTimedOut}).Stopped())
	assert.True(t, (&exec.Result{Outcome: exec.OutcomeCanceled}).Stopped())
}

func TestOutcomeString(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "completed", exec.OutcomeCompleted.String())
	assert.Equal(t, "command not found", exec.OutcomeNotFound.String())
	assert.Equal(t, "denied", exec.OutcomeDenied.String())
	assert.Equal(t, "timed out", exec.OutcomeTimedOut.String())
	assert.Equal(t, "canceled", exec.OutcomeCanceled.String())
	assert.Equal(t, "invalid request", exec.OutcomeInvalid.String())
	assert.Equal(t, "internal error", exec.OutcomeInternal.String())
	assert.Equal(t, "unsupported", exec.OutcomeUnsupported.String())
	assert.Equal(t, "outcome(42)", exec.Outcome(42).String())
}
