package builtins

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_mv(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		args    []string
		setup   func(t *testing.T, env *Env)
		wantErr bool
		check   func(t *testing.T, env *Env)
	}{
		"renames a file": {
			args: []string{"old", "new"},
			setup: func(t *testing.T, env *Env) {
				mustWrite(t, env.FS, "/work/old", "data")
			},
			check: func(t *testing.T, env *Env) {
				assert.Equal(t, "data", mustRead(t, env.FS, "/work/new"))
				exists, _ := afero.Exists(env.FS, "/work/old")
				assert.False(t, exists, "old should no longer exist")
			},
		},
		"moves into an existing directory using the base name": {
			args: []string{"f", "dst"},
			setup: func(t *testing.T, env *Env) {
				mustWrite(t, env.FS, "/work/f", "x")
				mustMkdir(t, env.FS, "/work/dst")
			},
			check: func(t *testing.T, env *Env) {
				assert.Equal(t, "x", mustRead(t, env.FS, "/work/dst/f"))
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

			env, _, _ := NewTestEnv(t, "/work")
			if tc.setup != nil {
				tc.setup(t, env)
			}

			err := mv(context.Background(), env, tc.args)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			if tc.check != nil {
				tc.check(t, env)
			}
		})
	}
}
