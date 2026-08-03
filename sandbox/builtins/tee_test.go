package builtins

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_tee(t *testing.T) {
	t.Parallel()

	inv, stdout, _ := NewTestEnv(t, "/work")
	inv.Stdin = strings.NewReader("payload")
	inv.Args = []string{"a.txt", "b.txt"}
	require.NoError(t, tee(context.Background(), inv))

	assert.Equal(t, "payload", stdout.String(), "stdout receives the input")
	assert.Equal(t, "payload", mustRead(t, inv.FS, "/work/a.txt"))
	assert.Equal(t, "payload", mustRead(t, inv.FS, "/work/b.txt"))
}

func Test_tee_truncatesByDefault(t *testing.T) {
	t.Parallel()

	inv, _, _ := NewTestEnv(t, "/work")
	mustWrite(t, inv.FS, "/work/out", "old content")
	inv.Stdin = strings.NewReader("new")
	inv.Args = []string{"out"}
	require.NoError(t, tee(context.Background(), inv))
	assert.Equal(t, "new", mustRead(t, inv.FS, "/work/out"))
}

func Test_tee_append(t *testing.T) {
	t.Parallel()

	inv, _, _ := NewTestEnv(t, "/work")
	mustWrite(t, inv.FS, "/work/out", "old")
	inv.Stdin = strings.NewReader("+new")
	inv.Args = []string{"-a", "out"}
	require.NoError(t, tee(context.Background(), inv))
	assert.Equal(t, "old+new", mustRead(t, inv.FS, "/work/out"))
}
