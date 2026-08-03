package builtins

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrtc0/sbsh/sandbox/command"
)

func Test_touch(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		setup   func(t *testing.T, inv *command.Invocation)
		args    []string
		wantErr bool
		check   func(t *testing.T, inv *command.Invocation)
	}{
		"creates an empty file": {
			args: []string{"f"},
			check: func(t *testing.T, inv *command.Invocation) {
				info, err := inv.FS.Stat("/work/f")
				require.NoError(t, err)
				assert.Equal(t, int64(0), info.Size(), "size")
			},
		},
		"does not truncate an existing file": {
			setup: func(t *testing.T, inv *command.Invocation) {
				mustWrite(t, inv.FS, "/work/f", "keep")
			},
			args: []string{"f"},
			check: func(t *testing.T, inv *command.Invocation) {
				assert.Equal(t, "keep", mustRead(t, inv.FS, "/work/f"), "content")
			},
		},
		"updates the modification time of an existing file": {
			setup: func(t *testing.T, inv *command.Invocation) {
				mustWrite(t, inv.FS, "/work/f", "x")
				old := time.Now().Add(-time.Hour)
				require.NoError(t, inv.FS.Chtimes("/work/f", old, old))
			},
			args: []string{"f"},
			check: func(t *testing.T, inv *command.Invocation) {
				old := time.Now().Add(-time.Hour)
				info, err := inv.FS.Stat("/work/f")
				require.NoError(t, err)
				assert.True(t, info.ModTime().After(old), "mtime = %v, want after %v", info.ModTime(), old)
			},
		},
		"errors with no arguments": {
			args:    nil,
			wantErr: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			inv, _, _ := NewTestEnv(t, "/work")
			if tc.setup != nil {
				tc.setup(t, inv)
			}

			inv.Args = tc.args
			err := touch(context.Background(), inv)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			if tc.check != nil {
				tc.check(t, inv)
			}
		})
	}
}
