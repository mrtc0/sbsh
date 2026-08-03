package builtins

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_sort(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		seed  map[string]string
		stdin string
		args  []string
		want  string
	}{
		"lexical sort of stdin": {
			stdin: "banana\napple\ncherry\n",
			args:  nil,
			want:  "apple\nbanana\ncherry\n",
		},
		"-r reverses": {
			stdin: "a\nb\nc\n",
			args:  []string{"-r"},
			want:  "c\nb\na\n",
		},
		"-n sorts numerically": {
			stdin: "10\n2\n1\n",
			args:  []string{"-n"},
			want:  "1\n2\n10\n",
		},
		"-u drops duplicates": {
			stdin: "b\na\nb\na\n",
			args:  []string{"-u"},
			want:  "a\nb\n",
		},
		"-f folds case": {
			stdin: "B\na\nC\n",
			args:  []string{"-f"},
			want:  "a\nB\nC\n",
		},
		"concatenates multiple files": {
			seed: map[string]string{"/work/a": "3\n1\n", "/work/b": "2\n"},
			args: []string{"-n", "a", "b"},
			want: "1\n2\n3\n",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			env, stdout, _ := NewTestEnv(t, "/work")
			for path, body := range tc.seed {
				mustWrite(t, env.FS, path, body)
			}
			if tc.seed == nil {
				env.Stdin = strings.NewReader(tc.stdin)
			}

			require.NoError(t, sortCommand(context.Background(), env, tc.args))
			assert.Equal(t, tc.want, stdout.String())
		})
	}
}
