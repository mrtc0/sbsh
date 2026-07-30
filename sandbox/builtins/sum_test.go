package builtins

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Known digests of the string "abc".
const (
	md5abc    = "900150983cd24fb0d6963f7d28e17f72"
	sha1abc   = "a9993e364706816aba3e25717850c26c9cd0d89d"
	sha256abc = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
)

func Test_checksums(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		fn   Func
		want string
	}{
		"md5sum":    {fn: md5sum, want: md5abc},
		"sha1sum":   {fn: sha1sum, want: sha1abc},
		"sha256sum": {fn: sha256sum, want: sha256abc},
	}

	for name, tc := range cases {
		t.Run(name+" reads a file", func(t *testing.T) {
			t.Parallel()
			env, stdout, _ := NewTestEnv(t, "/work")
			mustWrite(t, env.FS, "/work/f", "abc")
			require.NoError(t, tc.fn(context.Background(), env, []string{"f"}))
			assert.Equal(t, tc.want+"  f\n", stdout.String())
		})

		t.Run(name+" reads stdin as -", func(t *testing.T) {
			t.Parallel()
			env, stdout, _ := NewTestEnv(t, "/work")
			env.HC.Stdin = strings.NewReader("abc")
			require.NoError(t, tc.fn(context.Background(), env, nil))
			assert.Equal(t, tc.want+"  -\n", stdout.String())
		})
	}
}

func Test_checksums_multipleFiles(t *testing.T) {
	t.Parallel()

	env, stdout, _ := NewTestEnv(t, "/work")
	mustWrite(t, env.FS, "/work/a", "abc")
	mustWrite(t, env.FS, "/work/b", "abc")
	require.NoError(t, md5sum(context.Background(), env, []string{"a", "b"}))
	assert.Equal(t, md5abc+"  a\n"+md5abc+"  b\n", stdout.String())
}

func Test_checksums_missingFile(t *testing.T) {
	t.Parallel()

	env, _, _ := NewTestEnv(t, "/work")
	require.Error(t, sha256sum(context.Background(), env, []string{"nope"}))
}
