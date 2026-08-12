package repl

import (
	"context"
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
	if res != nil {
		// Whatever the script managed to write belongs to the user even when the
		// sandbox went on to fail, so the result is printed before the error.
		printResult(res, out, errw)
	}
	if err != nil {
		fmt.Fprintln(errw, "sandbox error:", err)
		return 1
	}
	return res.ExitCode
}

// Loop runs an interactive read-eval-print loop until the input stream is closed
// (Ctrl-D) or ctx ends, and returns the exit code for the process. Exit codes of
// individual scripts are not propagated; a loop that ended because ctx did
// reports [exitCodeTerminated].
//
// An interrupt delivered through [WithInterrupts] stops the running script and
// leaves the loop reading. One that arrives while the loop is waiting for input is
// dropped: interrupting a prompt has no script to stop.
//
// When the input is a terminal, line editing and in-session history
// (up/down arrows) are provided by golang.org/x/term, and Ctrl-C at the prompt
// cancels the line being typed rather than ending the session. Otherwise—for
// example when an agent pipes a script into stdin—it falls back to plain
// line-by-line reading, where Ctrl-C is not something the input can observe.
func (r *Runner) Loop(ctx context.Context, sb Executor) int {
	fmt.Fprintln(r.out, "sbsh REPL (Ctrl-D to exit)")

	if f, ok := r.in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		if code, handled := r.loopTerminal(ctx, sb, f); handled {
			return code
		}
		// Raw-mode setup failed; fall through to the plain reader.
	}
	return r.loop(ctx, sb, newScannerSource(r.in, r.out), r.out, r.err)
}

// loopTerminal drives the REPL with raw-mode line editing and history. It reports
// handled as false without consuming input if the terminal cannot be put into
// raw mode, so the caller can fall back to the plain reader.
func (r *Runner) loopTerminal(ctx context.Context, sb Executor, f *os.File) (code int, handled bool) {
	fd := int(f.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return 0, false
	}
	defer term.Restore(fd, oldState)

	src := newTerminalSource(r.in, r.out)
	// Route output through the terminal so newlines are translated to CRLF while
	// the terminal is in raw mode.
	t := src.Terminal()
	return r.loop(ctx, sb, src, t, t), true
}

// loop reads scripts from src and runs them until the input ends or ctx does.
//
// A cancelled input is absorbed here rather than turned into an exit, which is
// what keeps Ctrl-C at the prompt apart from the ways a session really ends.
func (r *Runner) loop(ctx context.Context, sb Executor, src lineSource, out, errw io.Writer) int {
	for {
		if ctx.Err() != nil {
			fmt.Fprintln(out)
			return exitCodeTerminated
		}

		line, kind, err := src.ReadLine()
		switch kind {
		case gotInterrupt:
			// Nothing was submitted and nothing is running, so there is nothing to
			// stop and no reason to leave: the source has already discarded what was
			// typed and put a fresh prompt up.
			r.dropPendingInterrupt()
			continue
		case gotEOF:
			if err != nil {
				fmt.Fprintln(errw, "read error:", err)
			}
			fmt.Fprintln(out)
			return 0
		}

		script := strings.TrimSpace(line)
		if script == "" {
			continue
		}
		// Asked again after the read, which is where the loop spends its time. A
		// line that arrives once the context has ended belongs to a session that is
		// already over; running it would only produce a script cancelled on the
		// spot.
		if ctx.Err() != nil {
			fmt.Fprintln(out)
			return exitCodeTerminated
		}
		r.dropPendingInterrupt()
		r.exec(ctx, sb, script, nil, out, errw)
	}
}

func printResult(res *sandbox.Result, out, errw io.Writer) {
	fmt.Fprint(out, res.Stdout)
	fmt.Fprint(errw, res.Stderr)
	if res.Truncated {
		fmt.Fprintln(errw, "(output truncated: the script wrote past the output limit; raise it with --output-limit)")
	}
	if res.ExitCode != 0 {
		fmt.Fprintf(errw, "(exit code %d)\n", res.ExitCode)
	}
}
