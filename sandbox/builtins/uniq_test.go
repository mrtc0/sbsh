package builtins

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_uniq(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		stdin string
		args  []string
		want  string
	}{
		"collapses adjacent duplicates": {
			stdin: "a\na\nb\nb\nb\nc\n",
			want:  "a\nb\nc\n",
		},
		"keeps non-adjacent duplicates": {
			stdin: "a\nb\na\n",
			want:  "a\nb\na\n",
		},
		"-c prefixes counts": {
			stdin: "a\na\nb\n",
			args:  []string{"-c"},
			want:  "      2 a\n      1 b\n",
		},
		"-d only duplicated": {
			stdin: "a\na\nb\n",
			args:  []string{"-d"},
			want:  "a\n",
		},
		"-u only unique": {
			stdin: "a\na\nb\n",
			args:  []string{"-u"},
			want:  "b\n",
		},
		"-i ignores case": {
			stdin: "Foo\nfoo\nbar\n",
			args:  []string{"-i"},
			want:  "Foo\nbar\n",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			env, stdout, _ := NewTestEnv(t, "/work")
			env.Stdin = strings.NewReader(tc.stdin)

			require.NoError(t, uniqCommand(context.Background(), env, tc.args))
			assert.Equal(t, tc.want, stdout.String())
		})
	}
}
