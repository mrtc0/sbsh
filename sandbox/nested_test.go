package sandbox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mrtc0/sh/v3/interp"

	"github.com/mrtc0/sbsh/sandbox/command"
	"github.com/mrtc0/sbsh/sandbox/exitcode"
)

// nester is a command that runs its argument as a child execution and keeps what
// came back, so a test can assert on the child's result rather than on whatever
// the outer script made of it.
//
// It takes the request from the test rather than from its arguments: everything
// a test wants to vary — the directory, the budget — lives on the request, and
// parsing that out of a command line would only be a second syntax to get wrong.
type nester struct {
	name string
	req  command.NestedRunRequest

	mu      sync.Mutex
	results []*command.NestedRunResult
	errs    []error
}

func (n *nester) Name() string        { return n.name }
func (n *nester) Description() string { return "run a script as a child execution" }

func (n *nester) Run(ctx context.Context, inv *command.Invocation) error {
	if inv.Nested == nil {
		return command.Exit(1, "nested execution is unavailable")
	}

	req := n.req
	if len(inv.Args) > 0 {
		req.Script = inv.Args[0]
	}

	res, err := inv.Nested.Run(ctx, req)

	n.mu.Lock()
	if err != nil {
		n.errs = append(n.errs, err)
	} else {
		n.results = append(n.results, res)
	}
	n.mu.Unlock()

	if err != nil {
		return command.Exitf(1, "%v", err)
	}
	return command.Exit(res.ExitCode)
}

// only returns the single result the command collected, failing the test when
// there is any other number of them.
func (n *nester) only(t *testing.T) *command.NestedRunResult {
	t.Helper()
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.errs) > 0 {
		t.Fatalf("nested run returned an error: %v", n.errs[0])
	}
	if len(n.results) != 1 {
		t.Fatalf("got %d nested results, want 1", len(n.results))
	}
	return n.results[0]
}

func (n *nester) firstErr(t *testing.T) error {
	t.Helper()
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.errs) == 0 {
		t.Fatalf("no nested run returned an error; got %d results", len(n.results))
	}
	return n.errs[0]
}

// newNestSandbox builds a sandbox with the nester registered as "nest".
func newNestSandbox(t *testing.T, opts ...Option) (*Sandbox, *nester) {
	t.Helper()
	n := &nester{name: "nest"}
	s, err := New(t.Context(), append(opts, WithCommand(n))...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s, n
}

// execOK runs script and fails the test when the sandbox itself could not.
func execOK(t *testing.T, s *Sandbox, script string) *Result {
	t.Helper()
	res, err := s.Exec(t.Context(), script, nil)
	if err != nil {
		t.Fatalf("Exec(%q): %v", script, err)
	}
	return res
}

// TestNestedRunReentrant is the deadlock check: the command is running inside
// Exec, which holds the sandbox's top-level lock, and starting a child from
// there must not queue behind it.
func TestNestedRunReentrant(t *testing.T) {
	t.Parallel()
	s, n := newNestSandbox(t)

	done := make(chan *Result, 1)
	go func() {
		res, err := s.Exec(t.Context(), `nest 'echo from the child'`, nil)
		if err != nil {
			t.Errorf("Exec: %v", err)
		}
		done <- res
	}()

	select {
	case res := <-done:
		if res.ExitCode != 0 {
			t.Errorf("exit code = %d, want 0; stderr: %s", res.ExitCode, res.Stderr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("nested execution deadlocked against the top-level lock")
	}

	if got, want := n.only(t).Stdout, "from the child\n"; got != want {
		t.Errorf("child stdout = %q, want %q", got, want)
	}
}

// TestNestedRunCapturesChildIO checks that the child's streams are its own: the
// parent's result must not carry what the child wrote.
func TestNestedRunCapturesChildIO(t *testing.T) {
	t.Parallel()
	s, n := newNestSandbox(t)

	res := execOK(t, s, `nest 'echo out; echo err >&2; exit 3'`)

	child := n.only(t)
	if got, want := child.Stdout, "out\n"; got != want {
		t.Errorf("child stdout = %q, want %q", got, want)
	}
	if got, want := child.Stderr, "err\n"; got != want {
		t.Errorf("child stderr = %q, want %q", got, want)
	}
	if child.ExitCode != 3 {
		t.Errorf("child exit code = %d, want 3", child.ExitCode)
	}
	if strings.Contains(res.Stdout, "out") || strings.Contains(res.Stderr, "err") {
		t.Errorf("child output leaked into the parent: stdout %q, stderr %q", res.Stdout, res.Stderr)
	}
	if res.ExitCode != 3 {
		t.Errorf("parent exit code = %d, want 3", res.ExitCode)
	}
}

func TestNestedRunExecutionMetadata(t *testing.T) {
	t.Parallel()
	s, n := newNestSandbox(t)

	execOK(t, s, `nest ':'`)

	child := n.only(t)
	if child.Depth != 1 {
		t.Errorf("depth = %d, want 1", child.Depth)
	}
	if child.ExecutionID == "" || child.ParentExecutionID == "" {
		t.Errorf("execution ids = %q / %q, want both set", child.ExecutionID, child.ParentExecutionID)
	}
	if child.ExecutionID == child.ParentExecutionID {
		t.Errorf("child and parent share the execution id %q", child.ExecutionID)
	}
}

func TestNestedRunInheritsDirWithoutLeakingIt(t *testing.T) {
	t.Parallel()
	s, n := newNestSandbox(t)

	res := execOK(t, s, `cd /tmp && nest 'pwd; cd /home/agent' && pwd`)

	if got, want := n.only(t).Stdout, "/tmp\n"; got != want {
		t.Errorf("child pwd = %q, want %q", got, want)
	}
	if got, want := res.Stdout, "/tmp\n"; got != want {
		t.Errorf("parent pwd after the child = %q, want %q", got, want)
	}
}

func TestNestedRunDirOverride(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		dir  string
		want string
	}{
		{name: "absolute", dir: "/home/agent", want: "/home/agent"},
		// Relative to where the calling command is, which the script has moved
		// to /tmp by then.
		{name: "relative to the caller", dir: "sub", want: "/tmp/sub"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, n := newNestSandbox(t)
			n.req.Dir = tc.dir

			execOK(t, s, `mkdir -p /tmp/sub && cd /tmp && nest 'pwd; echo $PWD'`)

			want := tc.want + "\n" + tc.want + "\n"
			if got := n.only(t).Stdout; got != want {
				t.Errorf("child stdout = %q, want %q", got, want)
			}
		})
	}
}

func TestNestedRunRejectsBadRequests(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		req    command.NestedRunRequest
		script string
		want   string
	}{
		{
			name:   "empty script",
			script: `nest ''`,
			want:   "script is empty",
		},
		{
			name:   "dir does not exist",
			req:    command.NestedRunRequest{Dir: "/nope"},
			script: `nest ':'`,
			want:   `dir "/nope"`,
		},
		{
			name:   "dir is a file",
			req:    command.NestedRunRequest{Dir: "/tmp/file"},
			script: `echo hi > /tmp/file && nest ':'`,
			want:   "not a directory",
		},
		{
			name:   "script does not parse",
			script: `nest 'if'`,
			want:   "parse:",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, n := newNestSandbox(t)
			n.req = tc.req

			execOK(t, s, tc.script)

			if err := n.firstErr(t); !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestNestedRunEnv(t *testing.T) {
	t.Parallel()
	s, n := newNestSandbox(t, WithEnv("FOO", "bar"))
	n.req.Env = []string{"BAZ=qux"}

	execOK(t, s, `nest 'echo $FOO $BAZ'`)

	if got, want := n.only(t).Stdout, "bar qux\n"; got != want {
		t.Errorf("child stdout = %q, want %q", got, want)
	}
}

// TestNestedRunInheritsExportedEnvOnly pins the boundary a child shell has: it
// gets what the parent exported, not everything the parent script could see.
func TestNestedRunInheritsExportedEnvOnly(t *testing.T) {
	t.Parallel()
	s, n := newNestSandbox(t)

	execOK(t, s, `PLAIN=set; export EXPORTED=set; nest 'echo "[$PLAIN][$EXPORTED]"'`)

	if got, want := n.only(t).Stdout, "[][set]\n"; got != want {
		t.Errorf("child stdout = %q, want %q", got, want)
	}
}

// TestNestedRunEnvOverrideIsExported checks that what the request adds carries
// on down, rather than stopping at the child it was given to.
func TestNestedRunEnvOverrideIsExported(t *testing.T) {
	t.Parallel()
	s, n := newNestSandbox(t)
	n.req.Env = []string{"HANDED_DOWN=yes"}

	execOK(t, s, `nest 'echo $HANDED_DOWN'`)

	if got, want := n.only(t).Stdout, "yes\n"; got != want {
		t.Errorf("child stdout = %q, want %q", got, want)
	}
}

func TestNestedRunEnvOverride(t *testing.T) {
	t.Parallel()
	s, n := newNestSandbox(t, WithEnv("FOO", "bar"))
	n.req.Env = []string{"FOO=overridden"}

	execOK(t, s, `nest 'echo $FOO'`)

	if got, want := n.only(t).Stdout, "overridden\n"; got != want {
		t.Errorf("child stdout = %q, want %q", got, want)
	}
}

func TestNestedRunOutputLimitClampsToParent(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		requested int64
		want      int
	}{
		{name: "request cannot loosen the parent limit", requested: 1 << 20, want: 8},
		{name: "request can tighten it", requested: 3, want: 3},
		{name: "zero leaves the parent limit", requested: 0, want: 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, n := newNestSandbox(t, WithOutputLimit(8))
			n.req.OutputLimit = tc.requested

			execOK(t, s, `nest 'echo 0123456789abcdef'`)

			child := n.only(t)
			if len(child.Stdout) != tc.want {
				t.Errorf("stdout = %q (%d bytes), want %d bytes", child.Stdout, len(child.Stdout), tc.want)
			}
			if !child.Truncated {
				t.Error("Truncated = false, want true")
			}
		})
	}
}

func TestNestedRunTimeoutClampsToParent(t *testing.T) {
	t.Parallel()
	// The child asks for far longer than the sandbox allows a run to take; the
	// parent's deadline is what has to stop it.
	s, n := newNestSandbox(t, WithTimeout(200*time.Millisecond))
	n.req.Timeout = time.Hour

	start := time.Now()
	execOK(t, s, `nest 'while true; do :; done'`)
	elapsed := time.Since(start)

	child := n.only(t)
	if !child.TimedOut {
		t.Error("TimedOut = false, want true")
	}
	if child.ExitCode != exitcode.Timeout {
		t.Errorf("exit code = %d, want %d", child.ExitCode, exitcode.Timeout)
	}
	if elapsed > 30*time.Second {
		t.Errorf("child ran for %v, so the parent's deadline did not clamp it", elapsed)
	}
}

func TestNestedRunOwnTimeout(t *testing.T) {
	t.Parallel()
	s, n := newNestSandbox(t, WithTimeout(30*time.Second))
	n.req.Timeout = 200 * time.Millisecond

	res := execOK(t, s, `nest 'while true; do :; done'; echo parent survived`)

	if !n.only(t).TimedOut {
		t.Error("TimedOut = false, want true")
	}
	// The child's own deadline is the child's: the parent had 30 seconds left
	// and has to still be running.
	if !strings.Contains(res.Stdout, "parent survived") {
		t.Errorf("parent stdout = %q, want the parent to have carried on", res.Stdout)
	}
}

func TestNestedRunCanceled(t *testing.T) {
	t.Parallel()
	s, n := newNestSandbox(t, WithTimeout(30*time.Second))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	go func() {
		// Give the child time to get going; the point is that a cancellation
		// reaching the parent reaches the child too.
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	if _, err := s.Exec(ctx, `nest 'while true; do :; done'`, nil); err != nil {
		t.Fatalf("Exec: %v", err)
	}

	child := n.only(t)
	if !child.Canceled {
		t.Errorf("Canceled = false, want true (TimedOut = %v)", child.TimedOut)
	}
	if child.ExitCode != exitcode.Canceled {
		t.Errorf("exit code = %d, want %d", child.ExitCode, exitcode.Canceled)
	}
}

// TestNormalizeNested drives the normalization directly, which is the only way
// to cover what the shell backend does not produce today: reporting a stop as an
// exit status rather than as an error of its own. TimedOut and Canceled have to
// survive that, or a caller would be left inferring a stop from a status.
func TestNormalizeNested(t *testing.T) {
	t.Parallel()

	expired := func() context.Context {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		t.Cleanup(cancel)
		return ctx
	}
	canceled := func() context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}

	for _, tc := range []struct {
		name   string
		ctx    context.Context
		runErr error
		want   command.NestedRunResult
	}{
		{
			name: "a clean run is left alone",
			ctx:  context.Background(),
		},
		{
			// The context expiring just after the last command finished must not
			// turn a success into a timeout.
			name: "a clean run under an expired context is still clean",
			ctx:  expired(),
		},
		{
			name:   "an exit status is the exit code",
			ctx:    context.Background(),
			runErr: interp.ExitStatus(3),
			want:   command.NestedRunResult{ExitCode: 3},
		},
		{
			name:   "a deadline reported as its own error",
			ctx:    expired(),
			runErr: context.DeadlineExceeded,
			want:   command.NestedRunResult{TimedOut: true, ExitCode: exitcode.Timeout},
		},
		{
			// What the backend returned says "exit 130"; only the context says
			// the child was stopped.
			name:   "a deadline reported as an exit status",
			ctx:    expired(),
			runErr: interp.ExitStatus(exitcode.Timeout),
			want:   command.NestedRunResult{TimedOut: true, ExitCode: exitcode.Timeout},
		},
		{
			name:   "a cancellation reported as an exit status",
			ctx:    canceled(),
			runErr: fmt.Errorf("wrapped: %w", interp.ExitStatus(1)),
			want:   command.NestedRunResult{Canceled: true, ExitCode: exitcode.Canceled},
		},
		{
			name:   "anything else is an internal error",
			ctx:    context.Background(),
			runErr: errors.New("the runtime came apart"),
			want:   command.NestedRunResult{InternalError: "the runtime came apart", ExitCode: 1},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var got command.NestedRunResult
			normalizeNested(tc.ctx, &got, tc.runErr)

			if got != tc.want {
				t.Errorf("normalizeNested() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// recurser nests into itself, so that the only thing stopping it is the depth
// guard.
type recurser struct {
	mu       sync.Mutex
	maxDepth int
	err      error
}

func (r *recurser) Name() string        { return "recurse" }
func (r *recurser) Description() string { return "nest into itself until the guard stops it" }

func (r *recurser) Run(ctx context.Context, inv *command.Invocation) error {
	if inv.Nested == nil {
		return command.Exit(1, "nested execution is unavailable")
	}
	res, err := inv.Nested.Run(ctx, command.NestedRunRequest{Script: "recurse"})

	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		if r.err == nil {
			r.err = err
		}
		return command.Exit(1)
	}
	if res.Depth > r.maxDepth {
		r.maxDepth = res.Depth
	}
	return command.Exit(res.ExitCode)
}

func TestNestedRunDepthGuard(t *testing.T) {
	t.Parallel()
	r := &recurser{}
	s, err := New(t.Context(), WithCommand(r))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	if _, err := s.Exec(t.Context(), "recurse", nil); err != nil {
		t.Fatalf("Exec: %v", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if !errors.Is(r.err, command.ErrMaxDepth) {
		t.Errorf("error = %v, want %v", r.err, command.ErrMaxDepth)
	}
	if r.maxDepth != command.MaxNestingDepth {
		t.Errorf("deepest child = %d, want %d", r.maxDepth, command.MaxNestingDepth)
	}
}

func TestNestedRunSeesSandboxFilesystem(t *testing.T) {
	t.Parallel()
	s, n := newNestSandbox(t)

	execOK(t, s, `echo written > /tmp/shared && nest 'cat /tmp/shared'`)

	if got, want := n.only(t).Stdout, "written\n"; got != want {
		t.Errorf("child stdout = %q, want %q", got, want)
	}
}

// TestNestedExecutorUnavailableOutsideASandboxRun covers the invocation a test
// or a host builds by hand: there is no execution to hang a child off, so the
// command is told so rather than handed an executor that cannot work.
func TestNestedExecutorUnavailableOutsideASandboxRun(t *testing.T) {
	t.Parallel()
	s, _ := newNestSandbox(t)

	if got := s.nestedExecutorFor(t.Context(), &command.Invocation{Name: "nest"}); got != nil {
		t.Errorf("nestedExecutorFor with no execution on the context = %v, want nil", got)
	}
}
