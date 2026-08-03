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

func Test_cp(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		args    []string
		setup   func(t *testing.T, env *Env)
		wantErr bool
		check   func(t *testing.T, env *Env)
	}{
		"copies a file to a new path": {
			args: []string{"src", "dst"},
			setup: func(t *testing.T, env *Env) {
				mustWrite(t, env.FS, "/work/src", "data")
			},
			check: func(t *testing.T, env *Env) {
				assert.Equal(t, "data", mustRead(t, env.FS, "/work/dst"))
				assert.Equal(t, "data", mustRead(t, env.FS, "/work/src"), "src should still exist")
			},
		},
		"copies into an existing directory using the base name": {
			args: []string{"src", "out"},
			setup: func(t *testing.T, env *Env) {
				mustWrite(t, env.FS, "/work/src", "x")
				mustMkdir(t, env.FS, "/work/out")
			},
			check: func(t *testing.T, env *Env) {
				assert.Equal(t, "x", mustRead(t, env.FS, "/work/out/src"))
			},
		},
		"refuses to copy a directory without -r": {
			args: []string{"dir", "copy"},
			setup: func(t *testing.T, env *Env) {
				mustMkdir(t, env.FS, "/work/dir")
			},
			wantErr: true,
		},
		"-r copies a directory tree": {
			args: []string{"-r", "dir", "copy"},
			setup: func(t *testing.T, env *Env) {
				mustWrite(t, env.FS, "/work/dir/a", "1")
				mustWrite(t, env.FS, "/work/dir/sub/b", "2")
			},
			check: func(t *testing.T, env *Env) {
				assert.Equal(t, "1", mustRead(t, env.FS, "/work/copy/a"))
				assert.Equal(t, "2", mustRead(t, env.FS, "/work/copy/sub/b"))
			},
		},
		"errors when copying multiple sources to a non-directory": {
			args: []string{"a", "b", "dst"},
			setup: func(t *testing.T, env *Env) {
				mustWrite(t, env.FS, "/work/a", "")
				mustWrite(t, env.FS, "/work/b", "")
			},
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
			err := cp(context.Background(), env)
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

func mustRead(t *testing.T, fs afero.Fs, path string) string {
	t.Helper()
	b, err := afero.ReadFile(fs, path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	return string(b)
}

// cp -r has to work against a host mount, not just MemMapFs: os.Root rejects a
// perm that carries type bits, which a directory's Mode() does.
func Test_cp_recursiveOnAHostMount(t *testing.T) {
	t.Parallel()

	env, hostDir, _, _ := NewTestEnvWithHostMount(t, "cp")
	require.NoError(t, os.MkdirAll(filepath.Join(hostDir, "src/sub"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(hostDir, "src/a.txt"), []byte("1"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(hostDir, "src/sub/b.txt"), []byte("2"), 0644))

	env.Args = []string{"-r", "/work/src", "/work/dst"}
	require.NoError(t, cp(context.Background(), env))

	got, err := os.ReadFile(filepath.Join(hostDir, "dst/a.txt"))
	require.NoError(t, err)
	assert.Equal(t, "1", string(got))
	got, err = os.ReadFile(filepath.Join(hostDir, "dst/sub/b.txt"))
	require.NoError(t, err)
	assert.Equal(t, "2", string(got))
}

// A denied source file must cost its own copy, not the whole tree's. GNU cp warns,
// copies the rest, and exits 1, leaving a partial copy behind.
func Test_cp_recursiveContinuesPastDeniedEntries(t *testing.T) {
	t.Parallel()

	env, base, _, stderr := NewTestEnvWithDeny(t, "/work", "cp", "**/.env")
	mustWrite(t, base, "/work/src/.env", "SECRET")
	mustWrite(t, base, "/work/src/a.txt", "1")
	mustWrite(t, base, "/work/src/sub/b.txt", "2")

	env.Args = []string{"-r", "/work/src", "/work/dst"}
	err := cp(context.Background(), env)

	var ee exitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, 1, ee.code)
	assert.Contains(t, stderr.String(), "cp:")
	assert.Contains(t, stderr.String(), "permission denied")

	assert.Equal(t, "1", mustRead(t, base, "/work/dst/a.txt"))
	assert.Equal(t, "2", mustRead(t, base, "/work/dst/sub/b.txt"))

	ok, err := afero.Exists(base, "/work/dst/.env")
	require.NoError(t, err)
	assert.False(t, ok, "the denied file must not be copied")
}
