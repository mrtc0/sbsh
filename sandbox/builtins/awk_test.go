package builtins

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrtc0/sbsh/sandbox/command"
)

func Test_awk(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		seed  map[string]string
		stdin string
		args  []string
		want  string
	}{
		"prints a field": {
			stdin: "a b c\nd e f\n",
			args:  []string{"{print $2}"},
			want:  "b\ne\n",
		},
		"NF and NR built-ins": {
			stdin: "one two\nthree\n",
			args:  []string{"{print NR, NF}"},
			want:  "1 2\n2 1\n",
		},
		"-F sets the field separator": {
			stdin: "a:b:c\n",
			args:  []string{"-F", ":", "{print $3}"},
			want:  "c\n",
		},
		"-F with a tab regex": {
			stdin: "a\tb\n",
			args:  []string{"-F", `\t`, "{print $2}"},
			want:  "b\n",
		},
		"pattern selects lines": {
			stdin: "keep 1\ndrop 2\nkeep 3\n",
			args:  []string{"/keep/{print $2}"},
			want:  "1\n3\n",
		},
		"numeric comparison pattern": {
			stdin: "1\n5\n3\n",
			args:  []string{"$1 > 2"},
			want:  "5\n3\n",
		},
		"-v passes a variable": {
			stdin: "x\n",
			args:  []string{"-v", "name=world", "{print name}"},
			want:  "world\n",
		},
		"BEGIN and END blocks": {
			stdin: "a\nb\nc\n",
			args:  []string{"BEGIN{print \"start\"} END{print NR}"},
			want:  "start\n3\n",
		},
		"sum with END": {
			stdin: "1\n2\n3\n",
			args:  []string{"{s += $1} END{print s}"},
			want:  "6\n",
		},
		"reads named files from the VFS": {
			seed: map[string]string{"/work/a": "1\n", "/work/b": "2\n3\n"},
			args: []string{"{print}", "a", "b"},
			want: "1\n2\n3\n",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			inv, stdout, _ := NewTestEnv(t, "/work")
			for path, body := range tc.seed {
				mustWrite(t, inv.FS, path, body)
			}
			if tc.seed == nil {
				inv.Stdin = strings.NewReader(tc.stdin)
			}

			inv.Args = tc.args
			require.NoError(t, awkCommand(context.Background(), inv))
			assert.Equal(t, tc.want, stdout.String())
		})
	}
}

func Test_awk_program_from_file(t *testing.T) {
	t.Parallel()

	inv, stdout, _ := NewTestEnv(t, "/work")
	mustWrite(t, inv.FS, "/work/prog.awk", "{print $1 + $2}\n")
	inv.Stdin = strings.NewReader("2 3\n10 20\n")

	inv.Args = []string{"-f", "prog.awk"}
	require.NoError(t, awkCommand(context.Background(), inv))
	assert.Equal(t, "5\n30\n", stdout.String())
}

func Test_awk_exitStatus(t *testing.T) {
	t.Parallel()

	inv, _, _ := NewTestEnv(t, "/work")
	inv.Stdin = strings.NewReader("")

	inv.Args = []string{"BEGIN{exit 3}"}
	err := awkCommand(context.Background(), inv)
	var ee *command.ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, 3, ee.Code)
}

// Test_awk_sandboxed asserts the interpreter cannot reach the host: process
// execution and host file I/O all fail closed.
func Test_awk_sandboxed(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"system() is blocked":     `BEGIN{system("echo hi")}`,
		"pipe to sh is blocked":   `BEGIN{print "x" | "sh"}`,
		"file write is blocked":   `BEGIN{print "x" > "/tmp/evil"}`,
		"getline file is blocked": `BEGIN{getline line < "/etc/passwd"; print line}`,
	}

	for name, prog := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			inv, _, _ := NewTestEnv(t, "/work")
			inv.Stdin = strings.NewReader("")
			inv.Args = []string{prog}
			require.Error(t, awkCommand(context.Background(), inv))
		})
	}
}

// Test_awk_contextCancel proves a runaway program is interrupted through the
// context, so the sandbox timeout can stop it.
func Test_awk_contextCancel(t *testing.T) {
	t.Parallel()

	inv, _, _ := NewTestEnv(t, "/work")
	inv.Stdin = strings.NewReader("")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	inv.Args = []string{"BEGIN{x=0; while (1) x++}"}
	done := make(chan error, 1)
	go func() {
		done <- awkCommand(ctx, inv)
	}()

	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("awk did not honor context cancellation")
	}
}
