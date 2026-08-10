package sandbox

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrtc0/sbsh/sandbox/command"
	"github.com/mrtc0/sbsh/sandbox/exec"
)

// This file validates the execution result contract end to end: what a caller
// is actually handed for each way a run can end, through both entry points.
// The pieces are tested where they live — the classification rules in
// sandbox/exec, the request checks in sandbox/command, the child runtime in
// nested_test.go — and what is left is whether the whole path reports the same
// thing, which is the part a host depends on and no single package can pin.
//
// Two properties run through it. An outcome is decided by what happened, not by
// which entry point ran it, so the same script classifies the same whether a
// host asked for it or a command did. And an outcome is a field: an exit code is
// shared by endings that mean different things, and stderr is prose, so neither
// can be what a caller branches on.

// entryPoint is one of the two ways a script is run in the sandbox. Every case
// below runs through both, which is what pins that a caller cannot tell them
// apart by what it is handed.
type entryPoint struct {
	name string
	// run evaluates script and returns the result and the error the contract
	// pairs with it.
	run func(t *testing.T, script string, opts ...Option) (*exec.Result, error)
}

func entryPoints() []entryPoint {
	return []entryPoint{
		{
			name: "a host calling the sandbox",
			run: func(t *testing.T, script string, opts ...Option) (*exec.Result, error) {
				t.Helper()

				s, err := New(t.Context(), opts...)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, s.Close()) })

				return s.Exec(t.Context(), script, nil)
			},
		},
		{
			name: "a command calling back into it",
			run: func(t *testing.T, script string, opts ...Option) (*exec.Result, error) {
				t.Helper()

				p := &nestedProbe{build: func(*command.Invocation) command.NestedRequest {
					return command.NestedRequest{Script: script}
				}}
				return runProbe(t, p, "probe", opts...), p.err
			},
		},
	}
}

// reporting is a registered command that ends on a status and a message of its
// own, which is how a host's command reports an ordinary failure.
func reporting() command.Command {
	return command.New("reporting", "exit non-zero with a message",
		func(context.Context, *command.Invocation) error {
			return command.Exit(7, "did not work")
		})
}

// TestOutcomeClassificationIsTheSameThroughBothEntryPoints walks the outcomes a
// script can reach and pins what each one reports. The table is deliberately
// free of timing: every case here is decided by what the script is, so it
// reproduces the same way every run. The endings that need a clock are below.
func TestOutcomeClassificationIsTheSameThroughBothEntryPoints(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		script      string
		opts        []Option
		wantOutcome exec.Outcome
		wantExit    int
		wantStdout  string
		wantStderr  string
		// wantErr is whether the contract pairs this ending with a Go error,
		// which it does only for a request that could never run and for a fault
		// of the sandbox.
		wantErr bool
	}{
		{
			name:        "a script that exits zero completed",
			script:      "echo hi",
			wantOutcome: exec.OutcomeCompleted,
			wantStdout:  "hi\n",
		},
		{
			name:        "a script that fails on its own completed with its status",
			script:      "echo before; exit 3",
			wantOutcome: exec.OutcomeCompleted,
			wantExit:    3,
			wantStdout:  "before\n",
		},
		{
			// A builtin's non-zero status is an answer, not a failure of
			// anything: grep exits 1 because there was no match.
			name:        "a builtin reporting no match completed",
			script:      "echo hi | grep nope",
			wantOutcome: exec.OutcomeCompleted,
			wantExit:    1,
		},
		{
			// A command the host registered reports through the same contract as
			// a builtin, and its message is printed the same way.
			name:        "a registered command's own status completed",
			script:      "reporting",
			opts:        []Option{WithCommand(reporting())},
			wantOutcome: exec.OutcomeCompleted,
			wantExit:    7,
			wantStderr:  "reporting: did not work\n",
		},
		{
			name:        "a name that does not resolve is a command not found",
			script:      "definitely-not-a-command",
			wantOutcome: exec.OutcomeNotFound,
			wantExit:    127,
			wantStderr:  "definitely-not-a-command: command not found\n",
		},
		{
			name:        "syntax the sandbox refuses is denied",
			script:      "cat <(echo hi)",
			wantOutcome: exec.OutcomeDenied,
			wantExit:    126,
			wantStderr:  "sbsh: process substitution is not allowed in the sandbox\n",
		},
		{
			name:        "a script that does not parse is an invalid request",
			script:      "if",
			wantOutcome: exec.OutcomeInvalid,
			wantExit:    2,
			wantErr:     true,
		},
	}

	for _, tc := range cases {
		for _, ep := range entryPoints() {
			t.Run(tc.name+", "+ep.name, func(t *testing.T) {
				t.Parallel()

				res, err := ep.run(t, tc.script, tc.opts...)

				require.NotNil(t, res, "a result is always populated")
				if tc.wantErr {
					require.Error(t, err, "%s comes with the detail as an error", tc.wantOutcome)
				} else {
					require.NoError(t, err, "%s is represented in a field, not an error", tc.wantOutcome)
				}
				assert.Equal(t, tc.wantOutcome, res.Outcome, "outcome")
				assert.Equal(t, tc.wantExit, res.ExitCode, "exit code")
				assert.Equal(t, tc.wantOutcome == exec.OutcomeCompleted && tc.wantExit == 0, res.OK(), "OK")
				assert.False(t, res.Stopped(), "nothing here was stopped by a limit")
				assert.Equal(t, tc.wantStdout, res.Stdout, "stdout")
				assert.Equal(t, tc.wantStderr, res.Stderr, "stderr")
			})
		}
	}
}

// TestOutcomesThatShareAnExitCodeStayApart is the case the outcome field exists
// for. A status is a shell's whole vocabulary, so several endings that a caller
// has to act on differently arrive under the same number: a name that did not
// resolve and a script choosing 127, a refusal and a script choosing 126, a
// request that could not be parsed and a script choosing 2. The pairs are run
// together so that the assertion is the distinction itself, not two numbers
// written down separately.
func TestOutcomesThatShareAnExitCodeStayApart(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		// classified is a script whose ending the sandbox classifies as
		// something other than a completed run.
		classified  string
		wantOutcome exec.Outcome
		// chosen is a script that completes on its own and picks the very same
		// status. Nothing distinguishes the two but the outcome.
		chosen   string
		wantExit int
	}{
		{
			name:        "a name that did not resolve and a script choosing 127",
			classified:  "definitely-not-a-command",
			wantOutcome: exec.OutcomeNotFound,
			chosen:      "definitely-not-a-command 2>/dev/null; exit 127",
			wantExit:    127,
		},
		{
			name:        "syntax the sandbox refused and a script choosing 126",
			classified:  "cat <(echo hi)",
			wantOutcome: exec.OutcomeDenied,
			chosen:      "exit 126",
			wantExit:    126,
		},
		{
			name:        "a request that does not parse and a script choosing 2",
			classified:  "if",
			wantOutcome: exec.OutcomeInvalid,
			chosen:      "exit 2",
			wantExit:    2,
		},
		{
			// 125 is the status of a sandbox fault. A script is free to pick it,
			// and doing so must not make the sandbox look broken.
			name:        "a script choosing the sandbox's own failure status",
			classified:  "",
			wantOutcome: exec.OutcomeInternal,
			chosen:      "exit 125",
			wantExit:    125,
		},
	}

	for _, tc := range cases {
		for _, ep := range entryPoints() {
			t.Run(tc.name+", "+ep.name, func(t *testing.T) {
				t.Parallel()

				chosen, err := ep.run(t, tc.chosen)
				require.NoError(t, err, "a script that completes is never an error")
				assert.Equal(t, exec.OutcomeCompleted, chosen.Outcome,
					"the script ran to the end, so the status is its own answer")
				assert.Equal(t, tc.wantExit, chosen.ExitCode)

				if tc.classified == "" {
					assert.NotEqual(t, tc.wantOutcome, chosen.Outcome,
						"a script picking the status of %s must not be reported as one", tc.wantOutcome)
					return
				}
				classified, _ := ep.run(t, tc.classified)
				assert.Equal(t, tc.wantOutcome, classified.Outcome)
				assert.Equal(t, chosen.ExitCode, classified.ExitCode,
					"the two endings are indistinguishable by status")
				assert.NotEqual(t, chosen.Outcome, classified.Outcome,
					"which is why the outcome is a field of its own")
			})
		}
	}
}

// TestARefusalAndALimitationShareAStatusAndNotAMeaning is the pair the table
// above cannot run, because one of the two is not something a script can be: the
// sandbox refusing a request and the sandbox having no way to carry one out both
// report 126, since a shell has no second status for "cannot execute". A caller
// telling a user which one happened has to read the outcome — "we would not" and
// "we cannot" are not the same news, and only one of them is worth arguing with.
func TestARefusalAndALimitationShareAStatusAndNotAMeaning(t *testing.T) {
	t.Parallel()

	var s *Sandbox
	var unsupported *exec.Result
	var unsupportedErr error
	// Calling Exec from inside the sandbox is the request the runtime cannot
	// carry out: the command would be waiting on the execution it is part of.
	reenter := command.New("reenter", "call Exec from inside the sandbox",
		func(ctx context.Context, _ *command.Invocation) error {
			unsupported, unsupportedErr = s.Exec(ctx, "echo hi", nil)
			return command.Exit(0)
		})

	s, err := New(t.Context(), WithCommand(reenter))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })

	denied, err := s.Exec(t.Context(), "cat <(echo hi)", nil)
	require.NoError(t, err, "a refusal is an outcome of a run, not a fault")
	_, err = s.Exec(t.Context(), "reenter", nil)
	require.NoError(t, err)
	require.NotNil(t, unsupported)
	require.NoError(t, unsupportedErr, "nor is a limitation of the runtime")

	assert.Equal(t, exec.OutcomeDenied, denied.Outcome)
	assert.Equal(t, exec.OutcomeUnsupported, unsupported.Outcome)
	assert.Equal(t, denied.ExitCode, unsupported.ExitCode, "the status cannot tell them apart")
	assert.Equal(t, 126, denied.ExitCode)
	// Both reasons are on stderr, because neither carries a Go error; what the
	// runtime cannot do says what to do instead, since asking differently will
	// not help.
	assert.Contains(t, denied.Stderr, "not allowed in the sandbox")
	assert.Contains(t, unsupported.Stderr, "nested execution")
}

// quitter waits for its context and then reports a status of its own, the way a
// well-written command reacts to being interrupted. It is the case a run that
// was stopped is easiest to lose: something has to end the run, and if what ends
// it is a status then the run reads as having completed.
func quitter() command.Command {
	return command.New("quitter", "exit with a status once the context ends",
		func(ctx context.Context, _ *command.Invocation) error {
			<-ctx.Done()
			return command.Exit(3, "interrupted")
		})
}

// TestARunThatWasStoppedNeverReportsCompleted pins that a limit ending a run is
// reported as such however the run ended, and through both entry points. A
// caller that cannot tell "it failed" from "we never let it finish" cannot
// decide whether retrying makes sense, and the status alone does not tell it:
// the quitter's 3 is an ordinary failure's status.
func TestARunThatWasStoppedNeverReportsCompleted(t *testing.T) {
	t.Parallel()

	const short = 20 * time.Millisecond

	cases := []struct {
		name        string
		run         func(t *testing.T) (*exec.Result, error)
		wantOutcome exec.Outcome
		wantExit    int
	}{
		{
			name: "the sandbox's timeout stops a host's run",
			run: func(t *testing.T) (*exec.Result, error) {
				s, err := New(t.Context(), WithCommand(quitter()), WithTimeout(short))
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, s.Close()) })

				return s.Exec(t.Context(), "quitter", nil)
			},
			wantOutcome: exec.OutcomeTimedOut,
			wantExit:    137,
		},
		{
			name: "the caller's cancellation stops a host's run",
			run: func(t *testing.T) (*exec.Result, error) {
				s, err := New(t.Context(), WithCommand(quitter()))
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, s.Close()) })

				ctx, cancel := context.WithCancel(t.Context())
				time.AfterFunc(short, cancel)
				return s.Exec(ctx, "quitter", nil)
			},
			wantOutcome: exec.OutcomeCanceled,
			wantExit:    130,
		},
		{
			name: "the requested timeout stops a nested run",
			run: func(t *testing.T) (*exec.Result, error) {
				p := &nestedProbe{build: func(*command.Invocation) command.NestedRequest {
					return command.NestedRequest{Script: "quitter", Timeout: short}
				}}
				res := runProbe(t, p, "probe", WithCommand(quitter()), WithTimeout(10*time.Second))
				return res, p.err
			},
			wantOutcome: exec.OutcomeTimedOut,
			wantExit:    137,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			res, err := tc.run(t)

			require.NoError(t, err, "a limit the caller asked for is not a failure of the sandbox")
			assert.Equal(t, tc.wantOutcome, res.Outcome,
				"the status the command chose on its way out is not why the run ended")
			assert.Equal(t, tc.wantExit, res.ExitCode)
			assert.True(t, res.Stopped())
			assert.False(t, res.OK())
		})
	}
}

// TestNothingAskedOfTheResultIsOnlyOnStderr pins the other half of "the outcome
// is a field": stderr is written for a person, so a caller that reads meaning
// out of it is reading something the sandbox never promised. A command can print
// whatever it likes there — including the exact wording the sandbox uses — and a
// classified ending stays classified when its stderr is thrown away.
func TestNothingAskedOfTheResultIsOnlyOnStderr(t *testing.T) {
	t.Parallel()

	// impostor prints the diagnostics of three endings it is not, and succeeds.
	impostor := command.New("impostor", "print other endings' diagnostics",
		func(_ context.Context, inv *command.Invocation) error {
			fmt.Fprint(inv.Stderr, "impostor: command not found\n")
			fmt.Fprint(inv.Stderr, "sbsh: process substitution is not allowed in the sandbox\n")
			fmt.Fprint(inv.Stderr, "sbsh: permission denied\n")
			return command.Exit(0)
		})

	for _, ep := range entryPoints() {
		t.Run(ep.name, func(t *testing.T) {
			t.Parallel()

			res, err := ep.run(t, "impostor", WithCommand(impostor))
			require.NoError(t, err)
			assert.True(t, res.OK(), "what a command prints does not decide how the run ended")
			assert.Equal(t, exec.OutcomeCompleted, res.Outcome)
			assert.Contains(t, res.Stderr, "command not found", "the wording really is there")

			// The endings whose reason is only readable on stderr, because no Go
			// error carries it, still say what they are in the field. Discarding
			// the stream loses the explanation and none of the meaning.
			notFound, err := ep.run(t, "definitely-not-a-command 2>/dev/null")
			require.NoError(t, err)
			assert.Equal(t, exec.OutcomeNotFound, notFound.Outcome)
			assert.Empty(t, notFound.Stderr)
		})
	}
}

// TestNestedRunsReachTheSameCommandsAsTheTopLevel covers the composition the
// nested surface exists for: builtins in a pipeline, a command the host
// registered, and that command nesting once more. What a caller sees of the
// whole tree is the outermost result, so the statuses have to travel back up
// through it.
func TestNestedRunsReachTheSameCommandsAsTheTopLevel(t *testing.T) {
	t.Parallel()

	// counter runs a nested script of its own and reports on what came back,
	// which makes it the middle level of a three-level tree.
	counter := command.New("counter", "count the lines a nested script prints",
		func(ctx context.Context, inv *command.Invocation) error {
			res, err := inv.RunNested(ctx, command.NestedRequest{Script: inv.Args[0]})
			if err != nil {
				return command.Exitf(1, "%v", err)
			}
			if !res.OK() {
				// A child's outcome is what a command branches on, and turning it
				// back into a status of its own is what makes a failure deep in
				// the tree visible at the top.
				return command.Exitf(res.ExitCode, "counting failed: %s", res.Outcome)
			}
			fmt.Fprintf(inv.Stdout, "%d\n", strings.Count(res.Stdout, "\n"))
			return command.Exit(0)
		})

	cases := []struct {
		name       string
		script     string
		wantExit   int
		wantStdout string
		wantStderr string
	}{
		{
			name:       "builtins compose in a nested pipeline",
			script:     "printf 'b\\na\\n' | sort | head -n 1",
			wantStdout: "a\n",
		},
		{
			name:       "a registered command runs in a nested script",
			script:     `counter "printf 'one\ntwo\n'"`,
			wantStdout: "2\n",
		},
		{
			// Two levels down a name does not resolve. The child that ran it
			// reports the ending, the command in between turns it into a status,
			// and that status is what the caller reads: a classification belongs
			// to the run it happened in and does not travel past the command that
			// asked for it. The child's own diagnostic stays with the child, which
			// is why the caller sees only what the command chose to say.
			name:       "a failure two levels down comes back as a status",
			script:     "counter definitely-not-a-command",
			wantExit:   127,
			wantStderr: "counter: counting failed: command not found\n",
		},
	}

	for _, tc := range cases {
		for _, ep := range entryPoints() {
			t.Run(tc.name+", "+ep.name, func(t *testing.T) {
				t.Parallel()

				res, err := ep.run(t, tc.script, WithCommand(counter))

				require.NoError(t, err)
				assert.Equal(t, exec.OutcomeCompleted, res.Outcome)
				assert.Equal(t, tc.wantExit, res.ExitCode)
				assert.Equal(t, tc.wantStdout, res.Stdout)
				assert.Equal(t, tc.wantStderr, res.Stderr)
			})
		}
	}
}

// TestTruncationIsIndependentOfWhyTheRunEnded pins that the output limit is
// reported next to the outcome rather than through it. A caller that must see
// everything has to check Truncated whatever the run reported, because a
// truncated run can have succeeded, failed on its own, or been stopped.
func TestTruncationIsIndependentOfWhyTheRunEnded(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		req         command.NestedRequest
		opts        []Option
		wantOutcome exec.Outcome
		wantExit    int
		wantOK      bool
	}{
		{
			name:        "a run that succeeded",
			req:         command.NestedRequest{Script: "printf abcdefgh", OutputLimit: 4},
			wantOutcome: exec.OutcomeCompleted,
			wantOK:      true,
		},
		{
			name:        "a run that failed on its own",
			req:         command.NestedRequest{Script: "printf abcdefgh; exit 3", OutputLimit: 4},
			wantOutcome: exec.OutcomeCompleted,
			wantExit:    3,
		},
		{
			name:        "a run whose diagnostics were what overflowed",
			req:         command.NestedRequest{Script: "printf abcdefgh >&2", OutputLimit: 4},
			wantOutcome: exec.OutcomeCompleted,
			wantOK:      true,
		},
		{
			name: "a run that was stopped",
			req: command.NestedRequest{
				Script:      "printf abcdefgh; block",
				OutputLimit: 4,
				Timeout:     20 * time.Millisecond,
			},
			opts:        []Option{WithCommand(blocker()), WithTimeout(10 * time.Second)},
			wantOutcome: exec.OutcomeTimedOut,
			wantExit:    137,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p := &nestedProbe{build: func(*command.Invocation) command.NestedRequest { return tc.req }}
			res := runProbe(t, p, "probe", tc.opts...)

			require.NoError(t, p.err)
			assert.True(t, res.Truncated, "the run wrote more than it was allowed to keep")
			assert.Equal(t, tc.wantOutcome, res.Outcome)
			assert.Equal(t, tc.wantExit, res.ExitCode)
			assert.Equal(t, tc.wantOK, res.OK(),
				"truncated output is not itself a failure; a caller that needs all of it decides that")
		})
	}
}

// TestADenialACommandRunsIntoIsNotADeniedRequest pins the line the contract
// draws around OutcomeDenied. The sandbox refusing to run a request at all is
// one thing; a command running into the sandbox's policy while it works is
// another, and it reports it the way it reports any other failure. Collapsing
// the two would tell a caller its request was refused when the request ran
// exactly as asked and the command inside it could not do its job.
func TestADenialACommandRunsIntoIsNotADeniedRequest(t *testing.T) {
	t.Parallel()

	mem := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(mem, "/token", []byte("secret"), 0o644))

	cases := []struct {
		name   string
		script string
		opts   []Option
	}{
		{
			name:   "a path the deny patterns cover",
			script: "cat /vault/token",
			opts:   []Option{WithMountRW("/vault", mem), WithDenyPaths("/vault/token")},
		},
		{
			name:   "a destination the network policy does not allow",
			script: "curl -s http://example.com",
		},
	}

	for _, tc := range cases {
		for _, ep := range entryPoints() {
			t.Run(tc.name+", "+ep.name, func(t *testing.T) {
				t.Parallel()

				res, err := ep.run(t, tc.script, tc.opts...)

				require.NoError(t, err)
				assert.Equal(t, exec.OutcomeCompleted, res.Outcome,
					"the request ran; the command inside it is what failed")
				assert.NotEqual(t, exec.OutcomeDenied, res.Outcome)
				assert.NotEqual(t, 0, res.ExitCode, "and it failed")
				assert.NotContains(t, res.Stdout, "secret")
			})
		}
	}
}

// TestInternalIsReservedForTheSandboxFailing pins the one outcome that says the
// result has nothing to do with what was asked for. It is worth nothing if
// anything else can reach it: a caller that sees it should be looking at the
// sandbox, not at its own request.
func TestInternalIsReservedForTheSandboxFailing(t *testing.T) {
	t.Parallel()

	// A command returning a plain error is outside the return contract — there
	// is no status on it to go by. That is the command breaking its side of the
	// bargain, not the sandbox failing, so dispatch reports it as an ordinary
	// failure of that command and the run completed.
	for _, ep := range entryPoints() {
		t.Run(ep.name, func(t *testing.T) {
			t.Parallel()

			res, err := ep.run(t, "broken", WithCommand(broken()))

			require.NoError(t, err, "an internal error is the only outcome that would carry one here")
			assert.Equal(t, exec.OutcomeCompleted, res.Outcome)
			assert.Equal(t, 1, res.ExitCode)
			assert.Equal(t, "broken: something went wrong\n", res.Stderr)
		})
	}
}
