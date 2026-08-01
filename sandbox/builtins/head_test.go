package builtins

import (
	"context"
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

			env, stdout, _ := NewTestEnv(t, "/work")
			for path, body := range tc.seed {
				mustWrite(t, env.FS, path, body)
			}
			if tc.stdin != "" {
				env.Stdin = strings.NewReader(tc.stdin)
			}

			env.Args = tc.args
			err := head(context.Background(), env)
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
