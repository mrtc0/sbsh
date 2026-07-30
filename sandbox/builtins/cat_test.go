package builtins

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_cat(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		seed       map[string]string
		stdin      string
		args       []string
		wantStdout string
		wantErr    bool
	}{
		"prints a single file": {
			seed:       map[string]string{"/work/a.txt": "hello\n"},
			args:       []string{"a.txt"},
			wantStdout: "hello\n",
		},
		"concatenates multiple files in order": {
			seed:       map[string]string{"/work/a": "A", "/work/b": "B"},
			args:       []string{"a", "b"},
			wantStdout: "AB",
		},
		"reads stdin when no path given": {
			stdin:      "from stdin",
			args:       nil,
			wantStdout: "from stdin",
		},
		"returns an error for a missing file": {
			args:    []string{"nope"},
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
				env.HC.Stdin = strings.NewReader(tc.stdin)
			}

			err := cat(context.Background(), env, tc.args)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantStdout, stdout.String(), "stdout")
		})
	}
}
