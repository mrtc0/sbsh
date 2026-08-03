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

	env, stdout, _ := NewTestEnv(t, "/work")
	env.Stdin = strings.NewReader("payload")
	require.NoError(t, tee(context.Background(), env, []string{"a.txt", "b.txt"}))

	assert.Equal(t, "payload", stdout.String(), "stdout receives the input")
	assert.Equal(t, "payload", mustRead(t, env.FS, "/work/a.txt"))
	assert.Equal(t, "payload", mustRead(t, env.FS, "/work/b.txt"))
}

func Test_tee_truncatesByDefault(t *testing.T) {
	t.Parallel()

	env, _, _ := NewTestEnv(t, "/work")
	mustWrite(t, env.FS, "/work/out", "old content")
	env.Stdin = strings.NewReader("new")
	require.NoError(t, tee(context.Background(), env, []string{"out"}))
	assert.Equal(t, "new", mustRead(t, env.FS, "/work/out"))
}

func Test_tee_append(t *testing.T) {
	t.Parallel()

	env, _, _ := NewTestEnv(t, "/work")
	mustWrite(t, env.FS, "/work/out", "old")
	env.Stdin = strings.NewReader("+new")
	require.NoError(t, tee(context.Background(), env, []string{"-a", "out"}))
	assert.Equal(t, "old+new", mustRead(t, env.FS, "/work/out"))
}
