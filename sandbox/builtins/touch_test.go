package builtins

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_touch(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		setup   func(t *testing.T, env *Env)
		args    []string
		wantErr bool
		check   func(t *testing.T, env *Env)
	}{
		"creates an empty file": {
			args: []string{"f"},
			check: func(t *testing.T, env *Env) {
				info, err := env.FS.Stat("/work/f")
				require.NoError(t, err)
				assert.Equal(t, int64(0), info.Size(), "size")
			},
		},
		"does not truncate an existing file": {
			setup: func(t *testing.T, env *Env) {
				mustWrite(t, env.FS, "/work/f", "keep")
			},
			args: []string{"f"},
			check: func(t *testing.T, env *Env) {
				assert.Equal(t, "keep", mustRead(t, env.FS, "/work/f"), "content")
			},
		},
		"updates the modification time of an existing file": {
			setup: func(t *testing.T, env *Env) {
				mustWrite(t, env.FS, "/work/f", "x")
				old := time.Now().Add(-time.Hour)
				require.NoError(t, env.FS.Chtimes("/work/f", old, old))
			},
			args: []string{"f"},
			check: func(t *testing.T, env *Env) {
				old := time.Now().Add(-time.Hour)
				info, err := env.FS.Stat("/work/f")
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

			env, _, _ := NewTestEnv(t, "/work")
			if tc.setup != nil {
				tc.setup(t, env)
			}

			err := touch(context.Background(), env, tc.args)
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
