package builtins

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_rm(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		setup   func(t *testing.T, env *Env)
		args    []string
		wantErr bool
		check   func(t *testing.T, env *Env)
	}{
		"removes a file": {
			setup: func(t *testing.T, env *Env) {
				mustWrite(t, env.FS, "/work/f", "x")
			},
			args: []string{"f"},
			check: func(t *testing.T, env *Env) {
				ok, _ := afero.Exists(env.FS, "/work/f")
				assert.False(t, ok, "f should be gone")
			},
		},
		"errors on a missing file without -f": {
			args:    []string{"nope"},
			wantErr: true,
		},
		"-f ignores a missing file": {
			args: []string{"-f", "nope"},
		},
		"refuses a directory without -r": {
			setup: func(t *testing.T, env *Env) {
				mustMkdir(t, env.FS, "/work/d")
			},
			args:    []string{"d"},
			wantErr: true,
		},
		"-r removes a directory tree": {
			setup: func(t *testing.T, env *Env) {
				mustWrite(t, env.FS, "/work/d/a", "1")
				mustWrite(t, env.FS, "/work/d/sub/b", "2")
			},
			args: []string{"-r", "d"},
			check: func(t *testing.T, env *Env) {
				ok, _ := afero.Exists(env.FS, "/work/d")
				assert.False(t, ok, "directory tree should be gone")
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			env, _, _ := NewTestEnv(t, "/work")
			if tc.setup != nil {
				tc.setup(t, env)
			}

			err := rm(context.Background(), env, tc.args)
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

// A denied entry must not turn a recursive delete into a no-op, and it must not
// disappear silently either. GNU rm removes what it may, reports what it may not,
// leaves the parents of a refused entry in place, and exits 1.
func Test_rm_recursiveContinuesPastDeniedEntries(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		seed        []string
		args        []string
		wantGone    []string
		wantKept    []string
		wantWarning string
		wantExit    int
	}{
		"a denied file is kept along with its parent": {
			seed:        []string{"sub/.env", "sub/a.txt"},
			args:        []string{"-r", "/work/sub"},
			wantGone:    []string{"sub/a.txt"},
			wantKept:    []string{"sub/.env", "sub"},
			wantWarning: "permission denied",
			wantExit:    1,
		},
		"-f does not hide the refusal": {
			seed:        []string{"sub/.env", "sub/a.txt"},
			args:        []string{"-rf", "/work/sub"},
			wantGone:    []string{"sub/a.txt"},
			wantKept:    []string{"sub/.env", "sub"},
			wantWarning: "permission denied",
			wantExit:    1,
		},
		"a directly named denied file is refused even with -f": {
			seed:        []string{".env"},
			args:        []string{"-f", "/work/.env"},
			wantKept:    []string{".env"},
			wantWarning: "permission denied",
			wantExit:    1,
		},
		"a tree with no denied entry is removed": {
			seed:     []string{"clean/b.txt", "clean/deep/c.txt"},
			args:     []string{"-r", "/work/clean"},
			wantGone: []string{"clean"},
		},
		"-f still ignores a missing path": {
			args: []string{"-f", "/work/missing"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			env, hostDir, _, stderr := NewTestEnvWithHostMount(t, "rm", "**/.env")
			for _, rel := range tc.seed {
				require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(hostDir, rel)), 0755))
				require.NoError(t, os.WriteFile(filepath.Join(hostDir, rel), []byte("x"), 0644))
			}

			err := rm(context.Background(), env, tc.args)

			if tc.wantExit == 0 {
				require.NoError(t, err)
				assert.Empty(t, stderr.String())
			} else {
				var ee exitError
				require.ErrorAs(t, err, &ee)
				assert.Equal(t, tc.wantExit, ee.code)
				assert.Contains(t, stderr.String(), tc.wantWarning)
				assert.Contains(t, stderr.String(), "rm:")
			}

			for _, rel := range tc.wantGone {
				_, err := os.Stat(filepath.Join(hostDir, rel))
				assert.True(t, os.IsNotExist(err), "%s should be gone", rel)
			}
			for _, rel := range tc.wantKept {
				_, err := os.Stat(filepath.Join(hostDir, rel))
				assert.NoError(t, err, "%s should be kept", rel)
			}
		})
	}
}
