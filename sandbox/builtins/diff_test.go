package builtins

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrtc0/sbsh/sandbox/command"
)

func Test_diff(t *testing.T) {
	t.Parallel()

	t.Run("identical files produce no output and exit 0", func(t *testing.T) {
		t.Parallel()
		inv, stdout, _ := NewTestEnv(t, "/work")
		mustWrite(t, inv.FS, "/work/a", "same\n")
		mustWrite(t, inv.FS, "/work/b", "same\n")

		inv.Args = []string{"a", "b"}
		require.NoError(t, diffCommand(context.Background(), inv))
		assert.Empty(t, stdout.String())
	})

	t.Run("emits a unified hunk and exits 1", func(t *testing.T) {
		t.Parallel()
		inv, stdout, _ := NewTestEnv(t, "/work")
		mustWrite(t, inv.FS, "/work/a", "line1\nline2\nline3\n")
		mustWrite(t, inv.FS, "/work/b", "line1\nCHANGED\nline3\n")

		inv.Args = []string{"a", "b"}
		err := diffCommand(context.Background(), inv)
		var ee *command.ExitError
		require.ErrorAs(t, err, &ee)
		assert.Equal(t, 1, ee.Code)

		want := "--- a\n+++ b\n@@ -1,3 +1,3 @@\n line1\n-line2\n+CHANGED\n line3\n"
		assert.Equal(t, want, stdout.String())
	})
}
