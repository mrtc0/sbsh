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

// nestedProbe is a command that runs one nested request and keeps what came
// back, which is how a test observes a child from outside the sandbox. The
// request is built from the invocation, so a case can vary it per call.
type nestedProbe struct {
	build func(inv *command.Invocation) command.NestedRequest

	res *exec.Result
	err error
}

func (p *nestedProbe) Name() string        { return "probe" }
func (p *nestedProbe) Description() string { return "run a nested script" }

func (p *nestedProbe) Run(ctx context.Context, inv *command.Invocation) error {
	req := command.NestedRequest{Script: strings.Join(inv.Args, " ")}
	if p.build != nil {
		req = p.build(inv)
	}
	p.res, p.err = inv.RunNested(ctx, req)
	if p.res != nil {
		fmt.Fprint(inv.Stdout, p.res.Stdout)
	}
	return command.Exit(0)
}

// runProbe executes script at the top level and returns what the probe's nested
// run reported.
func runProbe(t *testing.T, p *nestedProbe, script string, opts ...Option) *exec.Result {
	t.Helper()

	s, err := New(t.Context(), append(opts, WithCommand(p))...)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })

	_, err = s.Exec(t.Context(), script, nil)
	require.NoError(t, err)
	require.NotNil(t, p.res, "the probe never ran a nested script")
	return p.res
}

func TestNestedRunReportsWhatTheChildDid(t *testing.T) {
	t.Parallel()

	p := &nestedProbe{}
	res := runProbe(t, p, "probe 'echo one; echo two'")

	require.NoError(t, p.err)
	assert.True(t, res.OK())
	assert.Equal(t, "one\ntwo\n", res.Stdout)
	assert.Equal(t, exec.OutcomeCompleted, res.Outcome)
}

func TestNestedRunClassifiesTheChildsEnding(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		script      string
		wantOutcome exec.Outcome
		wantExit    int
	}{
		{"exits non-zero", "exit 3", exec.OutcomeCompleted, 3},
		{"command not found", "nosuchcommand", exec.OutcomeNotFound, 127},
		{"refused syntax", "cat <(echo hi)", exec.OutcomeDenied, 126},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p := &nestedProbe{}
			res := runProbe(t, p, fmt.Sprintf("probe %q", tc.script))

			require.NoError(t, p.err)
			assert.Equal(t, tc.wantOutcome, res.Outcome)
			assert.Equal(t, tc.wantExit, res.ExitCode)
		})
	}
}

func TestNestedRunReportsAScriptThatDoesNotParse(t *testing.T) {
	t.Parallel()

	p := &nestedProbe{}
	res := runProbe(t, p, `probe "if"`)

	require.Error(t, p.err)
	assert.Equal(t, exec.OutcomeInvalid, res.Outcome)
	assert.Equal(t, 2, res.ExitCode)
}

func TestNestedRunStartsWhereTheCallerStands(t *testing.T) {
	t.Parallel()

	p := &nestedProbe{}
	res := runProbe(t, p, "mkdir -p /work/sub && cd /work && probe pwd")

	require.NoError(t, p.err)
	assert.Equal(t, "/work\n", res.Stdout)
}

func TestNestedRunResolvesARequestedDirectory(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		dir  string
		want string
	}{
		{"relative to the caller", "sub", "/work/sub\n"},
		{"absolute", "/tmp", "/tmp\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p := &nestedProbe{build: func(*command.Invocation) command.NestedRequest {
				return command.NestedRequest{Script: "pwd", Dir: tc.dir}
			}}
			res := runProbe(t, p, "mkdir -p /work/sub && cd /work && probe")

			require.NoError(t, p.err)
			assert.Equal(t, tc.want, res.Stdout)
		})
	}
}

func TestNestedRunAgreesWithItselfAboutWhereItIs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		req  command.NestedRequest
		want string
	}{
		{
			name: "where the caller stands",
			req:  command.NestedRequest{Script: `echo "$(pwd) $PWD"`},
			want: "/work /work\n",
		},
		{
			name: "in a requested directory",
			req:  command.NestedRequest{Script: `echo "$(pwd) $PWD"`, Dir: "sub"},
			want: "/work/sub /work/sub\n",
		},
		{
			// PWD says where the child runs, so a request cannot make it say
			// otherwise.
			name: "with PWD asked for by the request",
			req:  command.NestedRequest{Script: `echo "$(pwd) $PWD"`, Env: []string{"PWD=/tmp"}},
			want: "/work /work\n",
		},
		{
			name: "after the child changes directory",
			req:  command.NestedRequest{Script: `cd sub; echo "$(pwd) $PWD"`},
			want: "/work/sub /work/sub\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p := &nestedProbe{build: func(*command.Invocation) command.NestedRequest { return tc.req }}
			res := runProbe(t, p, "mkdir -p /work/sub && cd /work && probe")

			require.NoError(t, p.err)
			assert.Equal(t, tc.want, res.Stdout)
		})
	}
}

func TestNestedRunDoesNotInheritTheCallersPreviousDirectory(t *testing.T) {
	t.Parallel()

	// The caller has been to /tmp, so its OLDPWD names it. A child that
	// inherited that could "cd -" its way into a directory it was never in.
	p := &nestedProbe{build: func(*command.Invocation) command.NestedRequest {
		return command.NestedRequest{Script: `echo "old=[$OLDPWD]"; cd sub; cd -; echo "$(pwd) $PWD"`}
	}}
	res := runProbe(t, p, "mkdir -p /work/sub && cd /tmp && cd /work && probe")

	require.NoError(t, p.err)
	assert.Equal(t, "old=[]\n/work\n/work /work\n", res.Stdout)
}

func TestNestedRunRejectsADirectoryThatIsNotThere(t *testing.T) {
	t.Parallel()

	p := &nestedProbe{build: func(*command.Invocation) command.NestedRequest {
		return command.NestedRequest{Script: "pwd", Dir: "/nowhere"}
	}}
	res := runProbe(t, p, "probe")

	require.Error(t, p.err)
	assert.Equal(t, exec.OutcomeInvalid, res.Outcome)
}

func TestNestedRunRefusesADirectoryThePolicyCovers(t *testing.T) {
	t.Parallel()

	mem := afero.NewMemMapFs()
	require.NoError(t, mem.MkdirAll("/secret", 0o755))

	p := &nestedProbe{build: func(*command.Invocation) command.NestedRequest {
		return command.NestedRequest{Script: "pwd", Dir: "/vault/secret"}
	}}
	res := runProbe(t, p, "probe", WithMountRW("/vault", mem), WithDenyPaths("/vault/secret"))

	require.NoError(t, p.err)
	assert.Equal(t, exec.OutcomeDenied, res.Outcome)
	assert.Contains(t, res.Stderr, "permission denied")
}

func TestNestedRunTakesItsEnvironmentFromTheRequest(t *testing.T) {
	t.Parallel()

	// The caller has a variable of its own and passes on one of its own
	// choosing. Only the second reaches the child: a script reads what the
	// request names, not what the calling shell happens to be carrying.
	p := &nestedProbe{build: func(*command.Invocation) command.NestedRequest {
		return command.NestedRequest{
			Script: `echo "left=[$LEFT_BEHIND] passed=[$PASSED_ON]"`,
			Env:    []string{"PASSED_ON=explicit"},
		}
	}}
	res := runProbe(t, p, "export LEFT_BEHIND=outer; probe")

	require.NoError(t, p.err)
	assert.Equal(t, "left=[] passed=[explicit]\n", res.Stdout)
}

func TestNestedRunGetsHomeAndPwdWithoutAsking(t *testing.T) {
	t.Parallel()

	// The two exceptions to the rule above, and the only ones: a script has no
	// other way to find the home directory, and PWD says where it is.
	p := &nestedProbe{build: func(*command.Invocation) command.NestedRequest {
		return command.NestedRequest{Script: `echo "home=[$HOME] pwd=[$PWD]"`}
	}}
	res := runProbe(t, p, "mkdir -p /work && cd /work && probe", WithEnv("HOME", "/home/someone"))

	require.NoError(t, p.err)
	assert.Equal(t, "home=[/home/someone] pwd=[/work]\n", res.Stdout)
}

func TestNestedRunSeesTheCallersHomeAsItStands(t *testing.T) {
	t.Parallel()

	// HOME is the caller's, not the sandbox's default: a caller that moved it
	// has a child that agrees with it.
	p := &nestedProbe{build: func(*command.Invocation) command.NestedRequest {
		return command.NestedRequest{Script: `echo "[$HOME]"`}
	}}
	res := runProbe(t, p, "export HOME=/home/moved; probe")

	require.NoError(t, p.err)
	assert.Equal(t, "[/home/moved]\n", res.Stdout)
}

func TestNestedRunRejectsAnEnvironmentEntryThatIsNotAPair(t *testing.T) {
	t.Parallel()

	p := &nestedProbe{build: func(*command.Invocation) command.NestedRequest {
		return command.NestedRequest{Script: "true", Env: []string{"NOTAPAIR"}}
	}}
	res := runProbe(t, p, "probe")

	require.Error(t, p.err)
	assert.Equal(t, exec.OutcomeInvalid, res.Outcome)
}

func TestNestedRunLeavesTheCallersSessionAlone(t *testing.T) {
	t.Parallel()

	p := &nestedProbe{build: func(*command.Invocation) command.NestedRequest {
		return command.NestedRequest{Script: "cd /tmp; export CHILD=set"}
	}}

	s, err := New(t.Context(), WithCommand(p))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })

	_, err = s.Exec(t.Context(), "mkdir -p /work && cd /work && probe", nil)
	require.NoError(t, err)

	// The child's directory and variables were its own; what it wrote to the
	// filesystem is shared, the way it is between two commands of a pipeline.
	res, err := s.Exec(t.Context(), "pwd; echo \"[$CHILD]\"", nil)
	require.NoError(t, err)
	assert.Equal(t, "/work\n[]\n", res.Stdout)
}

func TestNestedRunSharesTheSandboxFilesystem(t *testing.T) {
	t.Parallel()

	p := &nestedProbe{}

	s, err := New(t.Context(), WithCommand(p))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })

	_, err = s.Exec(t.Context(), "probe 'echo written > /tmp/child.txt'", nil)
	require.NoError(t, err)

	b, err := afero.ReadFile(s.FS(), "/tmp/child.txt")
	require.NoError(t, err)
	assert.Equal(t, "written\n", string(b))
}

func TestNestedRunIsBoundByTheDeniedPaths(t *testing.T) {
	t.Parallel()

	mem := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(mem, "/token", []byte("secret"), 0o644))

	p := &nestedProbe{}
	res := runProbe(t, p, "probe 'cat /vault/token'",
		WithMountRW("/vault", mem), WithDenyPaths("/vault/token"))

	require.NoError(t, p.err)
	assert.NotEqual(t, 0, res.ExitCode)
	assert.NotContains(t, res.Stdout, "secret")
}

func TestNestedRunClampsTheOutputLimit(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		requested   int64
		wantStdout  string
		wantTrunc   bool
		sandboxOpts []Option
	}{
		{
			name:        "a smaller request tightens the limit",
			requested:   4,
			wantStdout:  "abcd",
			wantTrunc:   true,
			sandboxOpts: []Option{WithOutputLimit(64)},
		},
		{
			name:        "a larger request does not loosen it",
			requested:   1 << 20,
			wantStdout:  "abcdefgh",
			wantTrunc:   true,
			sandboxOpts: []Option{WithOutputLimit(8)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p := &nestedProbe{build: func(*command.Invocation) command.NestedRequest {
				return command.NestedRequest{
					Script:      "printf abcdefghijklmnop",
					OutputLimit: tc.requested,
				}
			}}
			res := runProbe(t, p, "probe", tc.sandboxOpts...)

			require.NoError(t, p.err)
			assert.Equal(t, tc.wantStdout, res.Stdout)
			assert.Equal(t, tc.wantTrunc, res.Truncated)
		})
	}
}

// blocker waits for its context, which is how a test observes a child being
// stopped rather than finishing. The scripts below run a statement after it: the
// shell stops before that one, so the run ends on the deadline rather than on
// the status the blocked command happened to pick.
func blocker() command.Command {
	return command.New("block", "wait until the context ends",
		func(ctx context.Context, _ *command.Invocation) error {
			<-ctx.Done()
			return command.Exit(0)
		})
}

func TestNestedRunStopsAtTheRequestedTimeout(t *testing.T) {
	t.Parallel()

	p := &nestedProbe{build: func(*command.Invocation) command.NestedRequest {
		return command.NestedRequest{Script: "block; echo unreachable", Timeout: 20 * time.Millisecond}
	}}
	res := runProbe(t, p, "probe", WithCommand(blocker()), WithTimeout(10*time.Second))

	require.NoError(t, p.err)
	assert.Equal(t, exec.OutcomeTimedOut, res.Outcome)
	assert.Equal(t, 137, res.ExitCode)
}

func TestNestedRunIsStoppedByTheCallersDeadline(t *testing.T) {
	t.Parallel()

	// The request asks for far longer than the execution it hangs from has
	// left, which it cannot have: the earlier deadline is the one that stops it.
	p := &nestedProbe{build: func(*command.Invocation) command.NestedRequest {
		return command.NestedRequest{Script: "block; echo unreachable", Timeout: time.Hour}
	}}
	res := runProbe(t, p, "probe", WithCommand(blocker()), WithTimeout(20*time.Millisecond))

	require.NoError(t, p.err)
	assert.Equal(t, exec.OutcomeTimedOut, res.Outcome)
}

func TestNestedRunIsStoppedByTheCallersCancellation(t *testing.T) {
	t.Parallel()

	p := &nestedProbe{}

	s, err := New(t.Context(), WithCommand(p, blocker()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err = s.Exec(ctx, `probe 'block; echo unreachable'`, nil)
	require.NoError(t, err)
	require.NotNil(t, p.res)
	assert.Equal(t, exec.OutcomeCanceled, p.res.Outcome)
	assert.Equal(t, 130, p.res.ExitCode)
}

// recurser nests into itself, which is what the depth guard exists for. It
// reports the depth it reached and the outcome that ended the recursion.
type recurser struct{ deepest *exec.Result }

func (r *recurser) Name() string        { return "recurse" }
func (r *recurser) Description() string { return "nest into itself" }

func (r *recurser) Run(ctx context.Context, inv *command.Invocation) error {
	res, err := inv.RunNested(ctx, command.NestedRequest{Script: "recurse"})
	if err != nil {
		return command.Exitf(1, "%v", err)
	}
	if res.Outcome == exec.OutcomeDenied {
		r.deepest = res
	}
	fmt.Fprint(inv.Stdout, res.Stdout)
	return command.Exit(0)
}

func TestNestedRunStopsAtTheDepthGuard(t *testing.T) {
	t.Parallel()

	r := &recurser{}
	s, err := New(t.Context(), WithCommand(r))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })

	res, err := s.Exec(t.Context(), "recurse", nil)

	// The recursion terminates, and the run that would have gone past the guard
	// is refused rather than failing the sandbox.
	require.NoError(t, err)
	assert.True(t, res.OK())
	require.NotNil(t, r.deepest, "the recursion was never refused")
	assert.Equal(t, exec.OutcomeDenied, r.deepest.Outcome)
	assert.Contains(t, r.deepest.Stderr, "deep")
}

func TestNestedRunNumbersTheExecutionTree(t *testing.T) {
	t.Parallel()

	var ids []string
	report := command.New("report", "report where it runs",
		func(ctx context.Context, inv *command.Invocation) error {
			e, ok := exec.FromContext(ctx)
			require.True(t, ok, "a command always runs under an execution")
			ids = append(ids, fmt.Sprintf("%s@%d", e.ID, e.Depth))
			if len(inv.Args) > 0 {
				if _, err := inv.RunNested(ctx, command.NestedRequest{Script: "report"}); err != nil {
					return command.Exitf(1, "%v", err)
				}
			}
			return command.Exit(0)
		})

	s, err := New(t.Context(), WithCommand(report))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })

	_, err = s.Exec(t.Context(), "report nest; report nest", nil)
	require.NoError(t, err)
	_, err = s.Exec(t.Context(), "report", nil)
	require.NoError(t, err)

	// A child's ID carries its ancestry, and every execution is numbered in the
	// order it started, children included.
	assert.Equal(t, []string{"1@0", "1.2@1", "1@0", "1.3@1", "4@0"}, ids)
}

func TestExecRefusesToBeCalledFromInsideAnExecution(t *testing.T) {
	t.Parallel()

	var s *Sandbox
	var res *exec.Result
	var execErr error
	reenter := command.New("reenter", "call Exec from inside the sandbox",
		func(ctx context.Context, _ *command.Invocation) error {
			// Nothing waits: the call is refused before it reaches the session
			// the caller is holding.
			res, execErr = s.Exec(ctx, "echo hi", nil)
			return command.Exit(0)
		})

	s, err := New(t.Context(), WithCommand(reenter))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })

	_, err = s.Exec(t.Context(), "reenter", nil)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.NoError(t, execErr)
	assert.Equal(t, exec.OutcomeDenied, res.Outcome)
	assert.Contains(t, res.Stderr, "nested execution")
}
