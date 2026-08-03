package builtins

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_mkdir(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		setup   func(t *testing.T, env *Env)
		args    []string
		wantErr bool
		check   func(t *testing.T, env *Env)
	}{
		"creates a directory": {
			args: []string{"d"},
			check: func(t *testing.T, env *Env) {
				ok, _ := afero.DirExists(env.FS, "/work/d")
				assert.True(t, ok, "expected /work/d to be a directory")
			},
		},
		"without -p fails on an existing directory": {
			setup: func(t *testing.T, env *Env) {
				mustMkdir(t, env.FS, "/work/d")
			},
			args:    []string{"d"},
			wantErr: true,
		},
		"-p is idempotent on an existing directory": {
			setup: func(t *testing.T, env *Env) {
				mustMkdir(t, env.FS, "/work/d")
			},
			args: []string{"-p", "d"},
		},
		"-p creates parents": {
			args: []string{"-p", "a/b/c"},
			check: func(t *testing.T, env *Env) {
				ok, _ := afero.DirExists(env.FS, "/work/a/b/c")
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

			env, _, _ := NewTestEnv(t, "/work")
			if tc.setup != nil {
				tc.setup(t, env)
			}

			env.Args = tc.args
			err := mkdir(context.Background(), env)
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
