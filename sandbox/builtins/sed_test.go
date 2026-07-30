package builtins

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_sed(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		stdin string
		args  []string
		want  string
	}{
		"substitutes the first match per line": {
			stdin: "foo foo\nbar\n",
			args:  []string{"s/foo/baz/"},
			want:  "baz foo\nbar\n",
		},
		"g flag substitutes every match": {
			stdin: "foo foo\n",
			args:  []string{"s/foo/baz/g"},
			want:  "baz baz\n",
		},
		"i flag is case-insensitive": {
			stdin: "Foo\n",
			args:  []string{"s/foo/baz/i"},
			want:  "baz\n",
		},
		"backreferences and whole-match": {
			stdin: "2026-07-25\n",
			args:  []string{`s/([0-9]+)-([0-9]+)-([0-9]+)/\3\/\2\/\1 (&)/`},
			want:  "25/07/2026 (2026-07-25)\n",
		},
		"alternate delimiter": {
			stdin: "/usr/bin\n",
			args:  []string{"s#/usr#/opt#"},
			want:  "/opt/bin\n",
		},
		"d deletes matching lines": {
			stdin: "keep\ndrop me\nkeep\n",
			args:  []string{"/drop/d"},
			want:  "keep\nkeep\n",
		},
		"line-number address": {
			stdin: "a\nb\nc\n",
			args:  []string{"2d"},
			want:  "a\nc\n",
		},
		"range address": {
			stdin: "a\nb\nc\nd\n",
			args:  []string{"2,3d"},
			want:  "a\nd\n",
		},
		"-n with p prints only matches": {
			stdin: "one\ntwo\nthree\n",
			args:  []string{"-n", "/t/p"},
			want:  "two\nthree\n",
		},
		"multiple -e expressions": {
			stdin: "a\n",
			args:  []string{"-e", "s/a/b/", "-e", "s/b/c/"},
			want:  "c\n",
		},
		"preserves missing trailing newline": {
			stdin: "a\nb",
			args:  []string{"s/a/x/"},
			want:  "x\nb",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			env, stdout, _ := NewTestEnv(t, "/work")
			env.HC.Stdin = strings.NewReader(tc.stdin)

			require.NoError(t, sedCommand(context.Background(), env, tc.args))
			assert.Equal(t, tc.want, stdout.String())
		})
	}
}

func Test_sed_inPlace(t *testing.T) {
	t.Parallel()

	env, stdout, _ := NewTestEnv(t, "/work")
	mustWrite(t, env.FS, "/work/f", "hello world\nhello again\n")

	require.NoError(t, sedCommand(context.Background(), env, []string{"-i", "s/hello/hi/", "f"}))

	assert.Empty(t, stdout.String(), "-i must not write to stdout")
	assert.Equal(t, "hi world\nhi again\n", mustRead(t, env.FS, "/work/f"))
}

func Test_sed_error(t *testing.T) {
	t.Parallel()

	env, _, _ := NewTestEnv(t, "/work")
	env.HC.Stdin = strings.NewReader("x\n")

	require.Error(t, sedCommand(context.Background(), env, []string{"s/("}))
}
