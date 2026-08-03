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

	inv, stdout, _ := NewTestEnv(t, "/work")
	inv.Stdin = strings.NewReader("hello gzip")
	inv.Args = nil
	require.NoError(t, gzipCommand(context.Background(), inv))
	assert.Equal(t, "hello gzip", gunzipString(t, stdout.Bytes()))
}

func Test_gzip_file(t *testing.T) {
	t.Parallel()

	inv, _, _ := NewTestEnv(t, "/work")
	mustWrite(t, inv.FS, "/work/data.txt", "payload")
	inv.Args = []string{"data.txt"}
	require.NoError(t, gzipCommand(context.Background(), inv))

	// Original is replaced by data.txt.gz.
	_, err := inv.FS.Stat("/work/data.txt")
	assert.Error(t, err, "original should be removed")
	b, err := afero.ReadFile(inv.FS, "/work/data.txt.gz")
	require.NoError(t, err)
	assert.Equal(t, "payload", gunzipString(t, b))
}

func Test_gzip_keep(t *testing.T) {
	t.Parallel()

	inv, _, _ := NewTestEnv(t, "/work")
	mustWrite(t, inv.FS, "/work/data.txt", "payload")
	inv.Args = []string{"-k", "data.txt"}
	require.NoError(t, gzipCommand(context.Background(), inv))

	assert.Equal(t, "payload", mustRead(t, inv.FS, "/work/data.txt"), "original kept with -k")
	_, err := inv.FS.Stat("/work/data.txt.gz")
	require.NoError(t, err)
}

func Test_gunzip_file(t *testing.T) {
	t.Parallel()

	inv, _, _ := NewTestEnv(t, "/work")
	require.NoError(t, afero.WriteFile(inv.FS, "/work/data.txt.gz", gzipBytes(t, "restored"), 0o644))
	inv.Args = []string{"data.txt.gz"}
	require.NoError(t, gunzipCommand(context.Background(), inv))

	assert.Equal(t, "restored", mustRead(t, inv.FS, "/work/data.txt"))
	_, err := inv.FS.Stat("/work/data.txt.gz")
	assert.Error(t, err, "archive removed after decompression")
}

func Test_gunzip_stdin(t *testing.T) {
	t.Parallel()

	inv, stdout, _ := NewTestEnv(t, "/work")
	inv.Stdin = bytes.NewReader(gzipBytes(t, "from stdin"))
	inv.Args = nil
	require.NoError(t, gunzipCommand(context.Background(), inv))
	assert.Equal(t, "from stdin", stdout.String())
}

func Test_zcat(t *testing.T) {
	t.Parallel()

	inv, stdout, _ := NewTestEnv(t, "/work")
	require.NoError(t, afero.WriteFile(inv.FS, "/work/a.gz", gzipBytes(t, "cat me"), 0o644))
	inv.Args = []string{"a.gz"}
	require.NoError(t, zcatCommand(context.Background(), inv))

	assert.Equal(t, "cat me", stdout.String())
	// zcat keeps the input.
	_, err := inv.FS.Stat("/work/a.gz")
	require.NoError(t, err)
}

func Test_gunzip_badSuffix(t *testing.T) {
	t.Parallel()

	inv, _, _ := NewTestEnv(t, "/work")
	require.NoError(t, afero.WriteFile(inv.FS, "/work/data", gzipBytes(t, "x"), 0o644))
	inv.Args = []string{"data"}
	require.Error(t, gunzipCommand(context.Background(), inv))
}
