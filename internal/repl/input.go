package repl

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"

	"golang.org/x/term"
)

// readKind says how one attempt at reading a line ended. Keeping a cancelled
// input apart from a closed one is what lets the loop absorb Ctrl-C without
// borrowing the exit path that Ctrl-D uses.
type readKind int

const (
	// gotLine is a line the user submitted.
	gotLine readKind = iota
	// gotInterrupt is an input the user cancelled with Ctrl-C. Whatever was being
	// typed is gone, and the loop is expected to prompt again.
	gotInterrupt
	// gotEOF is the end of the input, whether that is Ctrl-D at a terminal or a
	// pipe that closed.
	gotEOF
)

// lineSource prompts for and reads the scripts a loop runs, one at a time.
type lineSource interface {
	// ReadLine returns the next line together with how the read ended. A line is
	// only meaningful for gotLine, and an error only accompanies gotEOF, and then
	// only when the input failed rather than ended.
	ReadLine() (string, readKind, error)
}

// prompt is what the loop shows while it waits for a script.
const prompt = "sbsh> "

// terminalSource reads lines with the editing and history that
// golang.org/x/term provides, and gives Ctrl-C its interactive meaning.
type terminalSource struct {
	t *term.Terminal
}

// newTerminalSource returns a source that reads keystrokes from in and echoes
// them to out. The terminal is expected to be in raw mode already; output written
// through [terminalSource.Terminal] is what keeps newlines translated to CRLF
// while it is.
func newTerminalSource(in io.Reader, out io.Writer) *terminalSource {
	rw := struct {
		io.Reader
		io.Writer
	}{&interruptReader{r: in}, out}
	return &terminalSource{t: term.NewTerminal(rw, prompt)}
}

// Terminal returns the writer the loop should print through, which is the
// terminal itself: it owns the cursor, so anything printed around it redraws the
// prompt and the line being edited.
func (s *terminalSource) Terminal() *term.Terminal { return s.t }

func (s *terminalSource) ReadLine() (string, readKind, error) {
	line, err := s.t.ReadLine()
	switch {
	case errors.Is(err, errInterrupted):
		// The line editor has already been driven back to an empty line, so the
		// echo is all that is left to do. Printing it through the terminal is what
		// puts the fresh prompt on screen.
		fmt.Fprintln(s.t, "^C")
		return "", gotInterrupt, nil
	case errors.Is(err, io.EOF):
		return "", gotEOF, nil
	case err != nil:
		return "", gotEOF, err
	}
	return line, gotLine, nil
}

// ctrlC is the byte a terminal in raw mode delivers for Ctrl-C: raw mode turns
// off the tty's own signal generation, so the keystroke arrives as input rather
// than as SIGINT, and giving it a meaning is the input layer's job.
const ctrlC = 0x03

// clearLine drives the line editor back to an empty line: end-of-line (^E)
// followed by erase-to-start (^U). golang.org/x/term keeps its buffer to itself,
// so keystrokes are how it is reset.
var clearLine = []byte{0x05, 0x15}

// errInterrupted reports an input the user cancelled. It travels as a read error
// because that is the only way out of [term.Terminal.ReadLine], and never leaves
// [terminalSource].
var errInterrupted = errors.New("input interrupted")

// interruptReader turns Ctrl-C into a cancelled read.
//
// golang.org/x/term answers Ctrl-C with io.EOF, which is also its answer to
// Ctrl-D. Taken at face value that makes an interrupt indistinguishable from a
// request to exit, which is how Ctrl-C used to end the REPL. Intercepting the
// byte before the line editor sees it is what keeps the two apart.
//
// One Ctrl-C is served over two reads: first the keystrokes that discard what was
// typed, then errInterrupted to break ReadLine out of its loop. That order is
// required, because ReadLine drops the bytes of a read that also returned an
// error.
type interruptReader struct {
	r       io.Reader
	pending []byte // keystrokes still owed to the line editor
	broken  bool   // once pending is drained, the next read reports the interrupt
}

func (r *interruptReader) Read(p []byte) (int, error) {
	if len(r.pending) > 0 {
		return r.serve(p), nil
	}
	if r.broken {
		r.broken = false
		return 0, errInterrupted
	}

	n, err := r.r.Read(p)
	if !bytes.Contains(p[:n], []byte{ctrlC}) {
		return n, err
	}

	// The whole chunk goes with the interrupt: what came before the Ctrl-C is what
	// the user asked to discard, and what came after it was typed for a prompt
	// that is already gone. A read error found alongside it is left for the next
	// read, which is where the loop will look once the interrupt is handled.
	r.pending = clearLine
	r.broken = true
	return r.serve(p), nil
}

// serve hands over as much of pending as fits, so that a small buffer only
// delays the interrupt rather than losing part of it.
func (r *interruptReader) serve(p []byte) int {
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n
}

// scannerSource reads lines without terminal features, for input that is a pipe
// or a file. Ctrl-C is not interpreted here: a 0x03 byte in a piped script is
// data, and an interrupt in that setting is a signal the process handles
// elsewhere.
type scannerSource struct {
	sc  *bufio.Scanner
	out io.Writer
}

func newScannerSource(in io.Reader, out io.Writer) *scannerSource {
	return &scannerSource{sc: bufio.NewScanner(in), out: out}
}

func (s *scannerSource) ReadLine() (string, readKind, error) {
	fmt.Fprint(s.out, prompt)
	if !s.sc.Scan() {
		return "", gotEOF, s.sc.Err()
	}
	return s.sc.Text(), gotLine, nil
}
