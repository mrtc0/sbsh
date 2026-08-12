package repl

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/mrtc0/sbsh/sandbox"
	"github.com/stretchr/testify/assert"
)

// The keystrokes a terminal in raw mode delivers, spelled out so that the tests
// below read as what the user pressed.
const (
	keyInterrupt = "\x03" // Ctrl-C
	keyEOF       = "\x04" // Ctrl-D
	keyLeft      = "\x1b[D"
	keyEnter     = "\r"
)

// keystrokes hands out one chunk per Read, which is how a terminal delivers
// them: a Ctrl-C arrives in a read of its own, and what was typed before it has
// already reached the line editor.
type keystrokes struct{ chunks []string }

func (k *keystrokes) Read(p []byte) (int, error) {
	if len(k.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, k.chunks[0])
	if n < len(k.chunks[0]) {
		k.chunks[0] = k.chunks[0][n:]
	} else {
		k.chunks = k.chunks[1:]
	}
	return n, nil
}

// recorder is an [Executor] that reports what it was asked to run.
type recorder struct{ scripts []string }

func (e *recorder) Exec(_ context.Context, script string, _ io.Reader) (*sandbox.Result, error) {
	e.scripts = append(e.scripts, script)
	return &sandbox.Result{}, nil
}

// TestLoop_terminalInterrupts pins what Ctrl-C means at the prompt: the line
// being typed is dropped and the session carries on, which is what separates it
// from Ctrl-D and from a closed input.
//
// The loop is driven over an in-memory terminal rather than a pty, because raw
// mode is the only part of the interactive path that needs a real one.
func TestLoop_terminalInterrupts(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		keys        []string
		wantScripts []string
		wantEchoes  int
	}{
		"an interrupt at an empty prompt does not end the loop": {
			keys:        []string{"echo one" + keyEnter, keyInterrupt, "echo two" + keyEnter},
			wantScripts: []string{"echo one", "echo two"},
			wantEchoes:  1,
		},
		"an interrupt mid-line leaves nothing behind": {
			keys:        []string{"half a scr", keyInterrupt, "echo two" + keyEnter},
			wantScripts: []string{"echo two"},
			wantEchoes:  1,
		},
		// ^E ^U is how the line editor is cleared, so a cursor left of the end is
		// what would expose a half-hearted reset.
		"an interrupt with the cursor mid-line leaves nothing behind": {
			keys:        []string{"abc", keyLeft, keyInterrupt, "echo two" + keyEnter},
			wantScripts: []string{"echo two"},
			wantEchoes:  1,
		},
		"repeated interrupts stay consistent": {
			keys:        []string{keyInterrupt, keyInterrupt, keyInterrupt, "echo one" + keyEnter},
			wantScripts: []string{"echo one"},
			wantEchoes:  3,
		},
		"Ctrl-D still ends the loop": {
			keys:        []string{"echo one" + keyEnter, keyEOF, "echo two" + keyEnter},
			wantScripts: []string{"echo one"},
		},
		// Ctrl-D only means end-of-input on an empty line, so an interrupt has to
		// leave the line empty for the two to keep their usual meanings.
		"Ctrl-D after an interrupt ends the loop": {
			keys:        []string{"half a scr", keyInterrupt, keyEOF, "echo two" + keyEnter},
			wantScripts: nil,
			wantEchoes:  1,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer
			in := &keystrokes{chunks: tc.keys}
			r := New(in, &out, &out)
			src := newTerminalSource(in, &out)
			exec := &recorder{}

			code := r.loop(context.Background(), exec, src, src.Terminal(), src.Terminal())

			assert.Equal(t, 0, code, "an interrupted input is not an exit code")
			assert.Equal(t, tc.wantScripts, exec.scripts)
			assert.Equal(t, tc.wantEchoes, strings.Count(out.String(), "^C"),
				"one echo per interrupt, and none without one")
		})
	}
}

// TestInterruptReader pins the two reads one Ctrl-C is served over. The order is
// what makes the reset work: golang.org/x/term drops the bytes of a read that
// also returned an error, so the keystrokes that clear the line have to arrive
// first.
func TestInterruptReader(t *testing.T) {
	t.Parallel()

	r := &interruptReader{r: &keystrokes{chunks: []string{"ab" + keyInterrupt + "cd", "ok"}}}

	buf := make([]byte, 64)
	n, err := r.Read(buf)
	assert.NoError(t, err)
	assert.Equal(t, clearLine, buf[:n], "the interrupted chunk is replaced by the reset")

	_, err = r.Read(buf)
	assert.ErrorIs(t, err, errInterrupted)

	n, err = r.Read(buf)
	assert.NoError(t, err)
	assert.Equal(t, "ok", string(buf[:n]), "reading resumes after the interrupt")
}
