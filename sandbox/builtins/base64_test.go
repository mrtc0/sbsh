package builtins

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_base64(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		seed  map[string]string
		stdin string
		args  []string
		want  string
	}{
		"encodes stdin": {
			stdin: "hello",
			args:  nil,
			want:  "aGVsbG8=\n",
		},
		"decodes with -d": {
			stdin: "aGVsbG8=",
			args:  []string{"-d"},
			want:  "hello",
		},
		"decode ignores embedded whitespace": {
			stdin: "aGVs\nbG8=\n",
			args:  []string{"-d"},
			want:  "hello",
		},
		"reads a file": {
			seed: map[string]string{"/work/f": "hi"},
			args: []string{"f"},
			want: "aGk=\n",
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
				env.HC.Stdin = strings.NewReader(tc.stdin)
			}

			require.NoError(t, base64Command(context.Background(), env, tc.args))
			assert.Equal(t, tc.want, stdout.String())
		})
	}
}

func Test_base64_wrap(t *testing.T) {
	t.Parallel()

	// 60 input bytes encode to 80 base64 characters, which wraps after 76.
	env, stdout, _ := NewTestEnv(t, "/work")
	env.HC.Stdin = strings.NewReader(strings.Repeat("a", 60))
	require.NoError(t, base64Command(context.Background(), env, nil))

	out := stdout.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	require.Len(t, lines, 2)
	assert.Len(t, lines[0], 76)

	// The wrapped output round-trips back to the original when decoded.
	env2, stdout2, _ := NewTestEnv(t, "/work")
	env2.HC.Stdin = strings.NewReader(out)
	require.NoError(t, base64Command(context.Background(), env2, []string{"-d"}))
	assert.Equal(t, strings.Repeat("a", 60), stdout2.String())
}

func Test_base64_noWrap(t *testing.T) {
	t.Parallel()

	env, stdout, _ := NewTestEnv(t, "/work")
	env.HC.Stdin = strings.NewReader(strings.Repeat("a", 60))
	require.NoError(t, base64Command(context.Background(), env, []string{"-w", "0"}))

	out := strings.TrimRight(stdout.String(), "\n")
	assert.NotContains(t, out, "\n")
	assert.Len(t, out, 80)
}

func Test_base64_invalidDecode(t *testing.T) {
	t.Parallel()

	env, _, _ := NewTestEnv(t, "/work")
	env.HC.Stdin = strings.NewReader("!!!not-base64!!!")
	require.Error(t, base64Command(context.Background(), env, []string{"-d"}))
}
