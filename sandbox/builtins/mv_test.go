package builtins

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrtc0/sbsh/sandbox/command"
)

func Test_mv(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		args    []string
		setup   func(t *testing.T, inv *command.Invocation)
		wantErr bool
		check   func(t *testing.T, inv *command.Invocation)
	}{
		"renames a file": {
			args: []string{"old", "new"},
			setup: func(t *testing.T, inv *command.Invocation) {
				mustWrite(t, inv.FS, "/work/old", "data")
			},
			check: func(t *testing.T, inv *command.Invocation) {
				assert.Equal(t, "data", mustRead(t, inv.FS, "/work/new"))
				exists, _ := afero.Exists(inv.FS, "/work/old")
				assert.False(t, exists, "old should no longer exist")
			},
		},
		"moves into an existing directory using the base name": {
			args: []string{"f", "dst"},
			setup: func(t *testing.T, inv *command.Invocation) {
				mustWrite(t, inv.FS, "/work/f", "x")
				mustMkdir(t, inv.FS, "/work/dst")
			},
			check: func(t *testing.T, inv *command.Invocation) {
				assert.Equal(t, "x", mustRead(t, inv.FS, "/work/dst/f"))
			},
		},
		"errors with too few arguments": {
			args:    []string{"only"},
			wantErr: true,
		},
		"errors when moving a missing source": {
			args:    []string{"nope", "dst"},
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
			err := mv(context.Background(), inv)
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
