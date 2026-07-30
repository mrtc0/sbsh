package repl_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/mrtc0/sbsh/internal/repl"
	"github.com/mrtc0/sbsh/sandbox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newSandbox(t *testing.T) *sandbox.Sandbox {
	t.Helper()
	sb, err := sandbox.New(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { sb.Close() })
	return sb
}

func TestRunner_Run(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		in       string
		script   string
		wantOut  string
		wantErr  string
		wantExit int
	}{
		"writes stdout and returns zero": {
			script:   "echo hello",
			wantOut:  "hello\n",
			wantExit: 0,
		},
		"hands its standard input to the script": {
			in:       "hello-from-host\n",
			script:   "cat",
			wantOut:  "hello-from-host\n",
			wantExit: 0,
		},
		"propagates the script exit code": {
			script:   "exit 3",
			wantExit: 3,
			wantErr:  "(exit code 3)",
		},
		"reports a sandbox error as exit 1": {
			script:   "cat <(echo hi)", // process substitution is rejected
			wantExit: 1,
			wantErr:  "sandbox error:",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var out, errBuf bytes.Buffer
			r := repl.New(strings.NewReader(tc.in), &out, &errBuf)

			code := r.Run(context.Background(), newSandbox(t), tc.script)

			assert.Equal(t, tc.wantExit, code)
			assert.Equal(t, tc.wantOut, out.String())
			if tc.wantErr != "" {
				assert.Contains(t, errBuf.String(), tc.wantErr)
			}
		})
	}
}

func TestRunner_Loop(t *testing.T) {
	t.Parallel()

	var out, errBuf bytes.Buffer
	in := strings.NewReader("echo one\n\necho two\n") // blank line is skipped
	r := repl.New(in, &out, &errBuf)

	r.Loop(context.Background(), newSandbox(t))

	got := out.String()
	assert.Contains(t, got, "sbsh REPL (Ctrl-D to exit)")
	assert.Contains(t, got, "one\n")
	assert.Contains(t, got, "two\n")
	// One prompt per read iteration: "echo one", the blank line, "echo two",
	// and the final read that hits EOF — four in total.
	assert.Equal(t, 4, strings.Count(got, "sbsh> "))
}

// fakeExecutor runs the function it is given. Deciding what a run does with its
// context is what lets the interrupt tests below be exact: a real script would
// have to be slow enough to interrupt, which is a race dressed up as a test.
type fakeExecutor struct {
	run func(ctx context.Context, script string, stdin io.Reader) (*sandbox.Result, error)
}

func (e *fakeExecutor) Exec(ctx context.Context, script string, stdin io.Reader) (*sandbox.Result, error) {
	return e.run(ctx, script, stdin)
}

// TestRunner_Run_interruptStopsTheScript pins that an interrupt reaches the
// running script. The outer context carries a deadline only so that a Runner that
// ignores interrupts fails instead of hanging; asserting on context.Canceled is
// what distinguishes the interrupt from that deadline.
func TestRunner_Run_interruptStopsTheScript(t *testing.T) {
	t.Parallel()

	interrupts := make(chan struct{}, 1)
	var ranWith error

	exec := &fakeExecutor{run: func(ctx context.Context, _ string, _ io.Reader) (*sandbox.Result, error) {
		interrupts <- struct{}{}
		<-ctx.Done()
		ranWith = ctx.Err()
		return &sandbox.Result{ExitCode: 130}, nil
	}}

	var out, errBuf bytes.Buffer
	r := repl.New(strings.NewReader(""), &out, &errBuf, repl.WithInterrupts(interrupts))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	assert.Equal(t, 130, r.Run(ctx, exec, "loop"))
	assert.ErrorIs(t, ranWith, context.Canceled, "the interrupt cancelled the run, not the deadline")
}

// TestRunner_Loop_interrupts pins that an interrupt is scoped to one script. It
// ends the script that is running and nothing else: the loop keeps reading, and an
// interrupt with no script to stop is not held against the next one.
func TestRunner_Loop_interrupts(t *testing.T) {
	t.Parallel()

	t.Run("the loop keeps reading after a script is interrupted", func(t *testing.T) {
		t.Parallel()

		interrupts := make(chan struct{}, 1)
		var scripts []string

		exec := &fakeExecutor{run: func(ctx context.Context, script string, _ io.Reader) (*sandbox.Result, error) {
			scripts = append(scripts, script)
			if len(scripts) == 1 {
				// The watcher is already selecting, and the channel is buffered, so
				// the send cannot deadlock against the wait that follows it.
				interrupts <- struct{}{}
				<-ctx.Done()
				return &sandbox.Result{ExitCode: 130}, nil
			}
			return &sandbox.Result{Stdout: "second ran\n"}, nil
		}}

		var out, errBuf bytes.Buffer
		r := repl.New(strings.NewReader("one\ntwo\n"), &out, &errBuf, repl.WithInterrupts(interrupts))

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		assert.Equal(t, 0, r.Loop(ctx, exec), "an interrupted script is not a reason to exit")
		assert.Equal(t, []string{"one", "two"}, scripts)
		assert.Contains(t, out.String(), "second ran\n")
	})

	t.Run("an interrupt pending before the first script is dropped", func(t *testing.T) {
		t.Parallel()

		interrupts := make(chan struct{}, 1)
		interrupts <- struct{}{} // as if Ctrl-C were pressed at the prompt

		var ranWith error
		exec := &fakeExecutor{run: func(ctx context.Context, _ string, _ io.Reader) (*sandbox.Result, error) {
			ranWith = ctx.Err()
			return &sandbox.Result{Stdout: "ok\n"}, nil
		}}

		var out, errBuf bytes.Buffer
		r := repl.New(strings.NewReader("echo ok\n"), &out, &errBuf, repl.WithInterrupts(interrupts))

		assert.Equal(t, 0, r.Loop(context.Background(), exec))
		assert.NoError(t, ranWith, "the pending interrupt must not have cancelled the next script")
		assert.Contains(t, out.String(), "ok\n")
	})

	t.Run("an interrupt arriving while waiting for input is dropped", func(t *testing.T) {
		t.Parallel()

		// The loop spends its time in the read, so that is where an interrupt at the
		// prompt lands. It has no script to stop, and must not be carried over to the
		// script submitted next.
		interrupts := make(chan struct{}, 1)

		var ranWith []error
		exec := &fakeExecutor{run: func(ctx context.Context, _ string, _ io.Reader) (*sandbox.Result, error) {
			ranWith = append(ranWith, ctx.Err())
			return &sandbox.Result{}, nil
		}}

		in := &lineReader{lines: []string{"echo one\n", "echo two\n"}, onRead: func(n int) {
			if n == 2 {
				interrupts <- struct{}{}
			}
		}}

		var out, errBuf bytes.Buffer
		r := repl.New(in, &out, &errBuf, repl.WithInterrupts(interrupts))

		assert.Equal(t, 0, r.Loop(context.Background(), exec))
		assert.Equal(t, []error{nil, nil}, ranWith, "neither script was cancelled")
	})

	t.Run("a finished context ends the loop without running anything", func(t *testing.T) {
		t.Parallel()

		var scripts []string
		exec := &fakeExecutor{run: func(_ context.Context, script string, _ io.Reader) (*sandbox.Result, error) {
			scripts = append(scripts, script)
			return &sandbox.Result{}, nil
		}}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		var out, errBuf bytes.Buffer
		r := repl.New(strings.NewReader("echo one\n"), &out, &errBuf)

		assert.Equal(t, 143, r.Loop(ctx, exec), "128 + SIGTERM")
		assert.Empty(t, scripts)
	})

	t.Run("a line read after the context ended is not run", func(t *testing.T) {
		t.Parallel()

		// The loop waits for input, so that is where a shutdown request arrives.
		// The reader ends the context as its second line is asked for, putting the
		// cancellation between the read and the run.
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)

		var scripts []string
		exec := &fakeExecutor{run: func(_ context.Context, script string, _ io.Reader) (*sandbox.Result, error) {
			scripts = append(scripts, script)
			return &sandbox.Result{}, nil
		}}

		in := &lineReader{lines: []string{"echo one\n", "echo two\n"}, onRead: func(n int) {
			if n == 2 {
				cancel()
			}
		}}

		var out, errBuf bytes.Buffer
		r := repl.New(in, &out, &errBuf)

		assert.Equal(t, 143, r.Loop(ctx, exec))
		assert.Equal(t, []string{"echo one"}, scripts, "the line read after the cancellation is discarded")
	})
}

// lineReader yields at most one line per Read, the way a pipe fed by a slow
// producer does. A strings.Reader would let the loop's bufio.Scanner buffer the
// whole input on its first read, which hides a script that steals the loop's
// reader.
type lineReader struct {
	lines []string
	// onRead, when set, is called with the 1-based index of each line as it is
	// handed out, which lets a test act at a chosen point in the loop.
	onRead func(line int)
	served int
}

func (r *lineReader) Read(p []byte) (int, error) {
	if len(r.lines) == 0 {
		return 0, io.EOF
	}
	if r.onRead != nil {
		r.served++
		r.onRead(r.served)
	}
	n := copy(p, r.lines[0])
	if n < len(r.lines[0]) {
		r.lines[0] = r.lines[0][n:]
	} else {
		r.lines = r.lines[1:]
	}
	return n, nil
}

// TestRunner_Loop_scriptDoesNotConsumeTheLoopsInput pins the ownership rule that
// makes threading standard input safe: in a loop the Runner's reader belongs to
// the line editor, so a script that reads standard input must not swallow the
// lines that follow it.
func TestRunner_Loop_scriptDoesNotConsumeTheLoopsInput(t *testing.T) {
	t.Parallel()

	var out, errBuf bytes.Buffer
	in := &lineReader{lines: []string{"cat\n", "echo two\n"}}
	r := repl.New(in, &out, &errBuf)

	r.Loop(context.Background(), newSandbox(t))

	got := out.String()
	// The second line ran as a command rather than being echoed back by cat.
	assert.Contains(t, got, "two\n")
	assert.NotContains(t, got, "echo two\n")
	// One prompt each for "cat", "echo two", and the read that hits EOF.
	assert.Equal(t, 3, strings.Count(got, "sbsh> "))
}
