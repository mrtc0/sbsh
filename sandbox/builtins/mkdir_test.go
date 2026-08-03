package builtins

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrtc0/sbsh/sandbox/command"
)

func Test_mkdir(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		setup   func(t *testing.T, inv *command.Invocation)
		args    []string
		wantErr bool
		check   func(t *testing.T, inv *command.Invocation)
	}{
		"creates a directory": {
			args: []string{"d"},
			check: func(t *testing.T, inv *command.Invocation) {
				ok, _ := afero.DirExists(inv.FS, "/work/d")
				assert.True(t, ok, "expected /work/d to be a directory")
			},
		},
		"without -p fails on an existing directory": {
			setup: func(t *testing.T, inv *command.Invocation) {
				mustMkdir(t, inv.FS, "/work/d")
			},
			args:    []string{"d"},
			wantErr: true,
		},
		"-p is idempotent on an existing directory": {
			setup: func(t *testing.T, inv *command.Invocation) {
				mustMkdir(t, inv.FS, "/work/d")
			},
			args: []string{"-p", "d"},
		},
		"-p creates parents": {
			args: []string{"-p", "a/b/c"},
			check: func(t *testing.T, inv *command.Invocation) {
				ok, _ := afero.DirExists(inv.FS, "/work/a/b/c")
				assert.True(t, ok, "expected /work/a/b/c to exist")
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
			err := mkdir(context.Background(), inv)
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
