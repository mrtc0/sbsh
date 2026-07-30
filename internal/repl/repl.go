package repl

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/mrtc0/sbsh/sandbox"
)

// Executor runs one script and reports its outcome. [sandbox.Sandbox] implements
// it. Naming the dependency as an interface is what lets a test drive a run that
// ends only when its context is cancelled, which is how the interrupt handling
// below is exercised without sending a real signal.
type Executor interface {
	Exec(ctx context.Context, script string, stdin io.Reader) (*sandbox.Result, error)
}

// exitCodeTerminated is what Loop reports when the context it was given ended,
// which is how a shutdown request reaches it. It follows the 128 + signal
// convention the sandbox uses for a script it stopped.
const exitCodeTerminated = 128 + 15 // SIGTERM

// Runner executes scripts in a sandbox and writes their output to the
// configured streams.
type Runner struct {
	in         io.Reader
	out        io.Writer
	err        io.Writer
	interrupts <-chan struct{}
}

// Option configures a Runner.
type Option func(*Runner)

// WithInterrupts makes the Runner stop the script it is running when ch receives.
//
// The channel stands for SIGINT, and what it cancels is the run rather than the
// Runner. That is what keeps a REPL usable across an interrupt: cancelling the
// context the caller passed in would end one script and leave every later one
// failing before it started.
func WithInterrupts(ch <-chan struct{}) Option {
	return func(r *Runner) { r.interrupts = ch }
}

// New returns a Runner that reads from in and writes to out and err.
func New(in io.Reader, out, err io.Writer, opts ...Option) *Runner {
	r := &Runner{in: in, out: out, err: err}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// interruptible returns a context an interrupt can cancel, together with a stop
// function that releases the goroutine watching for one. The watch lasts exactly
// as long as the run; what happens to an interrupt that arrives outside one is
// [Runner.dropPendingInterrupt]'s subject.
func (r *Runner) interruptible(ctx context.Context) (context.Context, func()) {
	if r.interrupts == nil {
		return ctx, func() {}
	}

	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		select {
		case <-r.interrupts:
			cancel()
		case <-done:
		}
	}()
	return ctx, func() {
		close(done)
		cancel()
	}
}

// dropPendingInterrupt discards an interrupt that arrived while the loop was
// waiting for input, and is therefore called after the read rather than before
// it: an interrupt left in the channel would be picked up by the next run's
// watcher the moment it starts, cancelling a script nobody interrupted.
//
// A nil channel is never ready, so this is a no-op when no interrupt source was
// configured.
func (r *Runner) dropPendingInterrupt() {
	for {
		select {
		case <-r.interrupts:
		default:
			return
		}
	}
}

// Run executes a single script, writes its output, and returns the exit code.
// A sandbox-level failure (as opposed to a non-zero script exit) is reported
// on the error stream and yields exit code 1.
func (r *Runner) Run(ctx context.Context, sb Executor, script string) int {
	return r.exec(ctx, sb, script, r.in, r.out, r.err)
}

// exec runs one script and writes its output to out and errw. The streams are
// passed explicitly so the interactive loop can route output through a
// terminal (which needs CRLF translation while in raw mode), and so each caller
// decides who owns the reader: a one-shot Run hands it to the script, while the
// loops keep it for the line editor and give the script an empty one.
func (r *Runner) exec(ctx context.Context, sb Executor, script string, stdin io.Reader, out, errw io.Writer) int {
	ctx, stop := r.interruptible(ctx)
	defer stop()

	res, err := sb.Exec(ctx, script, stdin)
	if err != nil {
		fmt.Fprintln(errw, "sandbox error:", err)
		return 1
	}
	printResult(res, out, errw)
	return res.ExitCode
}

// Loop runs an interactive read-eval-print loop until the input stream is closed
// (Ctrl-D) or ctx ends, and returns the exit code for the process. Exit codes of
// individual scripts are not propagated; a loop that ended because ctx did
// reports [exitCodeTerminated].
//
// An interrupt delivered through [WithInterrupts] stops the running script and
// leaves the loop reading. One that arrives while the loop is waiting for input is
// dropped: interrupting a prompt has nothing to stop, and the read cannot be
// broken off without closing the input.
//
// When the input is a terminal, line editing and in-session history
// (up/down arrows) are provided by golang.org/x/term. Otherwise—for example
// when an agent pipes a script into stdin—it falls back to plain line-by-line
// reading.
func (r *Runner) Loop(ctx context.Context, sb Executor) int {
	fmt.Fprintln(r.out, "sbsh REPL (Ctrl-D to exit)")

	if f, ok := r.in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		if code, handled := r.loopTerminal(ctx, sb, f); handled {
			return code
		}
		// Raw-mode setup failed; fall through to the plain reader.
	}
	return r.loopScanner(ctx, sb)
}

// loopTerminal drives the REPL with raw-mode line editing and history. It reports
// handled as false without consuming input if the terminal cannot be put into
// raw mode, so the caller can fall back to loopScanner.
func (r *Runner) loopTerminal(ctx context.Context, sb Executor, f *os.File) (code int, handled bool) {
	fd := int(f.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return 0, false
	}
	defer term.Restore(fd, oldState)

	// Terminal both reads keystrokes and echoes them, so it needs a combined
	// reader/writer over stdin and stdout.
	t := term.NewTerminal(struct {
		io.Reader
		io.Writer
	}{r.in, r.out}, "sbsh> ")

	for {
		if ctx.Err() != nil {
			fmt.Fprintln(t)
			return exitCodeTerminated, true
		}

		line, err := t.ReadLine()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				fmt.Fprintln(t, "read error:", err)
			}
			fmt.Fprintln(t)
			return 0, true
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		// Asked again after the read, for the reason given in loopScanner.
		if ctx.Err() != nil {
			fmt.Fprintln(t)
			return exitCodeTerminated, true
		}
		r.dropPendingInterrupt()
		// Route output through the terminal so newlines are translated to
		// CRLF while the terminal is in raw mode.
		r.exec(ctx, sb, line, nil, t, t)
	}
}

// loopScanner reads scripts line by line without terminal features.
func (r *Runner) loopScanner(ctx context.Context, sb Executor) int {
	sc := bufio.NewScanner(r.in)
	for {
		if ctx.Err() != nil {
			fmt.Fprintln(r.out)
			return exitCodeTerminated
		}

		fmt.Fprint(r.out, "sbsh> ")
		if !sc.Scan() {
			if err := sc.Err(); err != nil {
				fmt.Fprintln(r.err, "read error:", err)
			}
			fmt.Fprintln(r.out)
			return 0
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		// Asked again after the read, which is where the loop spends its time. A
		// line that arrives once the context has ended belongs to a session that is
		// already over; running it would only produce a script cancelled on the
		// spot.
		if ctx.Err() != nil {
			fmt.Fprintln(r.out)
			return exitCodeTerminated
		}
		r.dropPendingInterrupt()
		r.exec(ctx, sb, line, nil, r.out, r.err)
	}
}

func printResult(res *sandbox.Result, out, errw io.Writer) {
	fmt.Fprint(out, res.Stdout)
	fmt.Fprint(errw, res.Stderr)
	if res.Truncated {
		fmt.Fprintln(errw, "(output truncated)")
	}
	if res.ExitCode != 0 {
		fmt.Fprintf(errw, "(exit code %d)\n", res.ExitCode)
	}
}
