package builtins

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_head(t *testing.T) {
	t.Parallel()

	// 20 lines, used to verify the default 10-line limit.
	var b strings.Builder
	for i := 1; i <= 20; i++ {
		b.WriteString("line")
		b.WriteByte(byte('0' + i%10))
		b.WriteByte('\n')
	}
	twentyLines := b.String()

	cases := map[string]struct {
		seed          map[string]string
		stdin         string
		args          []string
		wantStdout    string
		wantContains  []string
		wantLineCount int
		checkLines    bool
		wantErr       bool
	}{
		"prints the first 10 lines by default": {
			seed:          map[string]string{"/work/f": twentyLines},
			args:          []string{"f"},
			wantLineCount: 10,
			checkLines:    true,
		},
		"-n limits the number of lines": {
			seed:       map[string]string{"/work/f": "a\nb\nc\nd\n"},
			args:       []string{"-n", "2", "f"},
			wantStdout: "a\nb\n",
		},
		"-nN combined form works": {
			seed:       map[string]string{"/work/f": "a\nb\nc\n"},
			args:       []string{"-n1", "f"},
			wantStdout: "a\n",
		},
		"negative -n keeps all but the last K lines": {
			seed:       map[string]string{"/work/f": "a\nb\nc\nd\n"},
			args:       []string{"-n", "-1", "f"},
			wantStdout: "a\nb\nc\n",
		},
		"negative -n larger than the file prints nothing": {
			seed:          map[string]string{"/work/f": "a\nb\n"},
			args:          []string{"-n", "-5", "f"},
			wantLineCount: 0,
			checkLines:    true,
		},
		"reads stdin when no file is given": {
			stdin:      "x\ny\nz\n",
			args:       []string{"-n", "1"},
			wantStdout: "x\n",
		},
		"multiple files get name headers": {
			seed:         map[string]string{"/work/a": "1\n", "/work/b": "2\n"},
			args:         []string{"a", "b"},
			wantContains: []string{"==> a <==", "==> b <=="},
		},
		"errors when -n has no value": {
			args:    []string{"-n"},
			wantErr: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			inv, stdout, _ := NewTestEnv(t, "/work")
			for path, body := range tc.seed {
				mustWrite(t, inv.FS, path, body)
			}
			if tc.stdin != "" {
				inv.Stdin = strings.NewReader(tc.stdin)
			}

			inv.Args = tc.args
			err := head(context.Background(), inv)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			out := stdout.String()
			if tc.checkLines {
				assert.Equal(t, tc.wantLineCount, strings.Count(out, "\n"), "line count")
			}
			if tc.wantStdout != "" {
				assert.Equal(t, tc.wantStdout, out, "stdout")
			}
			for _, sub := range tc.wantContains {
				assert.Contains(t, out, sub)
			}
		})
	}
}

func Test_head_leadingLineCount(t *testing.T) {
	t.Parallel()

	numbered := func(from, to int) string {
		var b strings.Builder
		for i := from; i <= to; i++ {
			fmt.Fprintf(&b, "l%d\n", i)
		}
		return b.String()
	}
	thirty := numbered(1, 30)

	cases := map[string]struct {
		seed       map[string]string
		stdin      string
		args       []string
		wantStdout string
		wantErr    bool
	}{
		"-N limits stdin": {
			stdin:      thirty,
			args:       []string{"-20"},
			wantStdout: numbered(1, 20),
		},
		"-N limits a file": {
			seed:       map[string]string{"/work/f": thirty},
			args:       []string{"-20", "f"},
			wantStdout: numbered(1, 20),
		},
		"-N with explicit stdin operand": {
			stdin:      thirty,
			args:       []string{"-3", "-"},
			wantStdout: numbered(1, 3),
		},
		"-N with multiple files prints headers": {
			seed:       map[string]string{"/work/a": thirty, "/work/b": thirty},
			args:       []string{"-2", "a", "b"},
			wantStdout: "==> a <==\nl1\nl2\n\n==> b <==\nl1\nl2\n",
		},
		"-0 prints nothing": {
			seed:       map[string]string{"/work/f": thirty},
			args:       []string{"-0", "f"},
			wantStdout: "",
		},
		"leading zeros are decimal": {
			seed:       map[string]string{"/work/f": thirty},
			args:       []string{"-020", "f"},
			wantStdout: numbered(1, 20),
		},
		"-N larger than the input prints everything": {
			seed:       map[string]string{"/work/f": "a\nb\n"},
			args:       []string{"-20", "f"},
			wantStdout: "a\nb\n",
		},
		"later -n wins": {
			seed:       map[string]string{"/work/f": thirty},
			args:       []string{"-20", "-n", "2", "f"},
			wantStdout: numbered(1, 2),
		},
		"later combined -nN wins": {
			seed:       map[string]string{"/work/f": thirty},
			args:       []string{"-20", "-n2", "f"},
			wantStdout: numbered(1, 2),
		},
		"later negative -n wins": {
			seed:       map[string]string{"/work/f": thirty},
			args:       []string{"-20", "-n", "-20", "f"},
			wantStdout: numbered(1, 10),
		},
		"-n -N still excludes the last N lines": {
			seed:       map[string]string{"/work/f": thirty},
			args:       []string{"-n", "-20", "f"},
			wantStdout: numbered(1, 10),
		},
		"-- ends options so -N is a file name": {
			seed:       map[string]string{"/work/-20": "x\ny\n"},
			args:       []string{"--", "-20"},
			wantStdout: "x\ny\n",
		},
		"-N then -- then a numeric file name": {
			seed:       map[string]string{"/work/-20": thirty},
			args:       []string{"-3", "--", "-20"},
			wantStdout: numbered(1, 3),
		},
		"huge -N overridden by a later -n is accepted": {
			seed:       map[string]string{"/work/f": thirty},
			args:       []string{"-99999999999999999999", "-n1", "f"},
			wantStdout: "l1\n",
		},
		"overflowing -N as the final value is an error": {
			seed:    map[string]string{"/work/f": thirty},
			args:    []string{"-99999999999999999999", "f"},
			wantErr: true,
		},
		"-N is only recognised as the first argument": {
			seed:    map[string]string{"/work/f": thirty},
			args:    []string{"f", "-20"},
			wantErr: true,
		},
		"-N after another option is an error": {
			seed:    map[string]string{"/work/f": thirty},
			args:    []string{"-n3", "-20", "f"},
			wantErr: true,
		},
		"two -N arguments are an error": {
			seed:    map[string]string{"/work/f": thirty},
			args:    []string{"-20", "-30", "f"},
			wantErr: true,
		},
		"-20x is not normalised": {
			args:    []string{"-20x"},
			wantErr: true,
		},
		"-2.0 is not normalised": {
			args:    []string{"-2.0"},
			wantErr: true,
		},
		"Unicode digits are not normalised": {
			args:    []string{"-２０"},
			wantErr: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			inv, stdout, _ := NewTestEnv(t, "/work")
			for path, body := range tc.seed {
				mustWrite(t, inv.FS, path, body)
			}
			if tc.stdin != "" {
				inv.Stdin = strings.NewReader(tc.stdin)
			}

			inv.Args = tc.args
			err := head(context.Background(), inv)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantStdout, stdout.String(), "stdout")
		})
	}
}

func Test_head_leadingLineCountKeepsArgs(t *testing.T) {
	t.Parallel()

	inv, _, _ := NewTestEnv(t, "/work")
	mustWrite(t, inv.FS, "/work/f", "a\nb\nc\n")
	args := []string{"-1", "f"}
	inv.Args = args

	require.NoError(t, head(context.Background(), inv))
	assert.Equal(t, []string{"-1", "f"}, args)
	assert.Equal(t, []string{"-1", "f"}, inv.Args)
}

func Test_tailAndParseLineCount_rejectLeadingLineCount(t *testing.T) {
	t.Parallel()

	inv, _, _ := NewTestEnv(t, "/work")
	mustWrite(t, inv.FS, "/work/f", "a\nb\nc\n")
	inv.Args = []string{"-2", "f"}
	require.Error(t, tail(context.Background(), inv))

	_, _, err := parseLineCount([]string{"-20"}, 10)
	require.Error(t, err)
}
