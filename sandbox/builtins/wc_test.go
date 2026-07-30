package builtins

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_wc(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		seed  map[string]string
		stdin string
		args  []string
		// wantFields matches strings.Fields of the whole output (single-line output).
		wantFields []string
		// wantLines / wantLastFields cover multi-line output (e.g. a total line).
		wantLines      int
		wantLastFields []string
	}{
		"counts lines, words and bytes by default": {
			seed:       map[string]string{"/work/f": "one two\nthree\n"},
			args:       []string{"f"},
			wantFields: []string{"2", "3", "14", "f"}, // lines words bytes name
		},
		"-l counts only lines": {
			seed:       map[string]string{"/work/f": "a\nb\nc\n"},
			args:       []string{"-l", "f"},
			wantFields: []string{"3", "f"},
		},
		"reads stdin when no file is given": {
			stdin:      "hello world\n",
			args:       []string{"-w"},
			wantFields: []string{"2"},
		},
		"multiple files add a total line": {
			seed:           map[string]string{"/work/a": "x\n", "/work/b": "y\nz\n"},
			args:           []string{"-l", "a", "b"},
			wantLines:      3,
			wantLastFields: []string{"3", "total"},
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
				env.HC.Stdin = strings.NewReader(tc.stdin)
			}

			require.NoError(t, wc(context.Background(), env, tc.args))

			if tc.wantLines > 0 {
				lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
				require.Len(t, lines, tc.wantLines, "output lines")
				last := strings.Fields(lines[len(lines)-1])
				assert.Equal(t, tc.wantLastFields, last, "total line")
			} else {
				assert.Equal(t, tc.wantFields, strings.Fields(stdout.String()), "fields")
			}
		})
	}
}
