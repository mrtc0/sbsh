package builtins

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func gzipBytes(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, err := w.Write([]byte(s))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return buf.Bytes()
}

func gunzipString(t *testing.T, b []byte) string {
	t.Helper()
	r, err := gzip.NewReader(bytes.NewReader(b))
	require.NoError(t, err)
	out, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(out)
}

func Test_gzip_stdinToStdout(t *testing.T) {
	t.Parallel()

	env, stdout, _ := NewTestEnv(t, "/work")
	env.Stdin = strings.NewReader("hello gzip")
	require.NoError(t, gzipCommand(context.Background(), env, nil))
	assert.Equal(t, "hello gzip", gunzipString(t, stdout.Bytes()))
}

func Test_gzip_file(t *testing.T) {
	t.Parallel()

	env, _, _ := NewTestEnv(t, "/work")
	mustWrite(t, env.FS, "/work/data.txt", "payload")
	require.NoError(t, gzipCommand(context.Background(), env, []string{"data.txt"}))

	// Original is replaced by data.txt.gz.
	_, err := env.FS.Stat("/work/data.txt")
	assert.Error(t, err, "original should be removed")
	b, err := afero.ReadFile(env.FS, "/work/data.txt.gz")
	require.NoError(t, err)
	assert.Equal(t, "payload", gunzipString(t, b))
}

func Test_gzip_keep(t *testing.T) {
	t.Parallel()

	env, _, _ := NewTestEnv(t, "/work")
	mustWrite(t, env.FS, "/work/data.txt", "payload")
	require.NoError(t, gzipCommand(context.Background(), env, []string{"-k", "data.txt"}))

	assert.Equal(t, "payload", mustRead(t, env.FS, "/work/data.txt"), "original kept with -k")
	_, err := env.FS.Stat("/work/data.txt.gz")
	require.NoError(t, err)
}

func Test_gunzip_file(t *testing.T) {
	t.Parallel()

	env, _, _ := NewTestEnv(t, "/work")
	require.NoError(t, afero.WriteFile(env.FS, "/work/data.txt.gz", gzipBytes(t, "restored"), 0o644))
	require.NoError(t, gunzipCommand(context.Background(), env, []string{"data.txt.gz"}))

	assert.Equal(t, "restored", mustRead(t, env.FS, "/work/data.txt"))
	_, err := env.FS.Stat("/work/data.txt.gz")
	assert.Error(t, err, "archive removed after decompression")
}

func Test_gunzip_stdin(t *testing.T) {
	t.Parallel()

	env, stdout, _ := NewTestEnv(t, "/work")
	env.Stdin = bytes.NewReader(gzipBytes(t, "from stdin"))
	require.NoError(t, gunzipCommand(context.Background(), env, nil))
	assert.Equal(t, "from stdin", stdout.String())
}

func Test_zcat(t *testing.T) {
	t.Parallel()

	env, stdout, _ := NewTestEnv(t, "/work")
	require.NoError(t, afero.WriteFile(env.FS, "/work/a.gz", gzipBytes(t, "cat me"), 0o644))
	require.NoError(t, zcatCommand(context.Background(), env, []string{"a.gz"}))

	assert.Equal(t, "cat me", stdout.String())
	// zcat keeps the input.
	_, err := env.FS.Stat("/work/a.gz")
	require.NoError(t, err)
}

func Test_gunzip_badSuffix(t *testing.T) {
	t.Parallel()

	env, _, _ := NewTestEnv(t, "/work")
	require.NoError(t, afero.WriteFile(env.FS, "/work/data", gzipBytes(t, "x"), 0o644))
	require.Error(t, gunzipCommand(context.Background(), env, []string{"data"}))
}
