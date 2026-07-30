package vfs_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/mrtc0/sbsh/vfs"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// denyPatterns is the set used by every test here: a name pattern that applies
// at any depth, and a directory whose whole subtree is off limits.
var denyPatterns = []string{"**/.env", "/work/secrets"}

func seedDenyFS(t *testing.T) *vfs.DenyFS {
	t.Helper()

	base := vfs.NewVFS(afero.NewMemMapFs())
	require.NoError(t, afero.WriteFile(base, "/work/.env", []byte("SECRET=1"), 0644))
	require.NoError(t, afero.WriteFile(base, "/work/main.go", []byte("package main"), 0644))
	require.NoError(t, afero.WriteFile(base, "/work/secrets/db/pass.txt", []byte("pw"), 0644))

	deny, err := vfs.NewDenyFS(base, denyPatterns)
	require.NoError(t, err)
	return deny
}

func TestNewDenyFS_RejectsInvalidPattern(t *testing.T) {
	t.Parallel()

	_, err := vfs.NewDenyFS(vfs.NewVFS(afero.NewMemMapFs()), []string{"[bad"})
	assert.Error(t, err)
}

func TestDenyFS_DeniedPathRejected(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		op func(fs afero.Fs, name string) error
	}{
		"Open": {
			op: func(fs afero.Fs, name string) error {
				_, err := fs.Open(name)
				return err
			},
		},
		"OpenFile": {
			op: func(fs afero.Fs, name string) error {
				_, err := fs.OpenFile(name, os.O_RDONLY, 0644)
				return err
			},
		},
		"Create": {
			op: func(fs afero.Fs, name string) error {
				_, err := fs.Create(name)
				return err
			},
		},
		"Stat": {
			op: func(fs afero.Fs, name string) error {
				_, err := fs.Stat(name)
				return err
			},
		},
		"Mkdir": {
			op: func(fs afero.Fs, name string) error { return fs.Mkdir(name, 0755) },
		},
		"MkdirAll": {
			op: func(fs afero.Fs, name string) error { return fs.MkdirAll(name, 0755) },
		},
		"Remove": {
			op: func(fs afero.Fs, name string) error { return fs.Remove(name) },
		},
		"RemoveAll": {
			op: func(fs afero.Fs, name string) error { return fs.RemoveAll(name) },
		},
		"Chmod": {
			op: func(fs afero.Fs, name string) error { return fs.Chmod(name, 0600) },
		},
		"Chown": {
			op: func(fs afero.Fs, name string) error { return fs.Chown(name, 0, 0) },
		},
		"Chtimes": {
			op: func(fs afero.Fs, name string) error {
				return fs.Chtimes(name, time.Unix(0, 0), time.Unix(0, 0))
			},
		},
	}

	// Every denied path form: a direct match, a match at another depth, the
	// denied directory itself, and a file below it.
	names := []string{"/work/.env", "/.env", "/work/secrets", "/work/secrets/db/pass.txt"}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			deny := seedDenyFS(t)
			for _, n := range names {
				err := tc.op(deny, n)
				require.Error(t, err, "%s on %s", name, n)
				assert.ErrorIs(t, err, syscall.EACCES, "%s on %s", name, n)
			}

			// The denied files are still intact underneath.
			got, err := afero.ReadFile(deny, "/work/main.go")
			require.NoError(t, err)
			assert.Equal(t, "package main", string(got))
		})
	}
}

func TestDenyFS_AllowedPathPassesThrough(t *testing.T) {
	t.Parallel()

	deny := seedDenyFS(t)

	got, err := afero.ReadFile(deny, "/work/main.go")
	require.NoError(t, err)
	assert.Equal(t, "package main", string(got))

	require.NoError(t, afero.WriteFile(deny, "/work/new.txt", []byte("x"), 0644))
	require.NoError(t, deny.MkdirAll("/work/sub/dir", 0755))

	fi, err := deny.Stat("/work/new.txt")
	require.NoError(t, err)
	assert.Equal(t, int64(1), fi.Size())

	// ".envrc" is not ".env": the pattern must not match by prefix.
	require.NoError(t, afero.WriteFile(deny, "/work/.envrc", []byte("x"), 0644))
}

func TestDenyFS_RenameChecksBothEnds(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		oldname string
		newname string
	}{
		"source denied":      {oldname: "/work/.env", newname: "/work/copy.txt"},
		"destination denied": {oldname: "/work/main.go", newname: "/work/.env"},
		"both denied":        {oldname: "/work/.env", newname: "/work/secrets/x"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			deny := seedDenyFS(t)

			err := deny.Rename(tc.oldname, tc.newname)
			require.Error(t, err)
			assert.ErrorIs(t, err, syscall.EACCES)
		})
	}
}

func TestDenyFS_RenameBetweenAllowedPathsSucceeds(t *testing.T) {
	t.Parallel()

	deny := seedDenyFS(t)

	require.NoError(t, deny.Rename("/work/main.go", "/work/renamed.go"))

	got, err := afero.ReadFile(deny, "/work/renamed.go")
	require.NoError(t, err)
	assert.Equal(t, "package main", string(got))
}

// seedSubtreeFS builds a tree whose denied entries sit below the directories the
// tests act on, so a check limited to the named path lets them through.
func seedSubtreeFS(t *testing.T, patterns []string) (*vfs.DenyFS, afero.Fs) {
	t.Helper()

	base := vfs.NewVFS(afero.NewMemMapFs())
	require.NoError(t, afero.WriteFile(base, "/work/sub/.env", []byte("SECRET=1"), 0644))
	require.NoError(t, afero.WriteFile(base, "/work/sub/a.txt", []byte("a"), 0644))
	require.NoError(t, afero.WriteFile(base, "/work/secrets/db/pass.txt", []byte("pw"), 0644))
	require.NoError(t, afero.WriteFile(base, "/work/clean/b.txt", []byte("b"), 0644))

	deny, err := vfs.NewDenyFS(base, patterns)
	require.NoError(t, err)
	return deny, base
}

// RemoveAll deletes a whole subtree, so a denied file must not be reachable by
// naming one of its parents.
func TestDenyFS_RemoveAllChecksTheSubtree(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		patterns []string
		target   string
		wantErr  bool
	}{
		"denied file below the target": {
			patterns: denyPatterns,
			target:   "/work/sub",
			wantErr:  true,
		},
		"denied directory below the target": {
			patterns: denyPatterns,
			target:   "/work",
			wantErr:  true,
		},
		"denied entry deeper down": {
			patterns: []string{"/work/secrets/db/pass.txt"},
			target:   "/work",
			wantErr:  true,
		},
		"no denied entry in the subtree": {
			patterns: denyPatterns,
			target:   "/work/clean",
		},
		"pattern cannot select anything below the target": {
			patterns: []string{"/elsewhere/**"},
			target:   "/work/sub",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			deny, base := seedSubtreeFS(t, tc.patterns)

			err := deny.RemoveAll(tc.target)
			if !tc.wantErr {
				require.NoError(t, err)
				_, err := base.Stat(tc.target)
				assert.Error(t, err, "the subtree should be gone")
				return
			}

			require.Error(t, err)
			assert.ErrorIs(t, err, syscall.EACCES)

			// Nothing was removed: the refusal leaves the tree as it was.
			_, err = base.Stat("/work/sub/.env")
			assert.NoError(t, err)
			_, err = base.Stat("/work/sub/a.txt")
			assert.NoError(t, err)
		})
	}
}

// RemoveAll on a missing path succeeds, matching os.RemoveAll. The subtree check
// must not turn that into a refusal.
func TestDenyFS_RemoveAllMissingPathSucceeds(t *testing.T) {
	t.Parallel()

	deny, _ := seedSubtreeFS(t, denyPatterns)

	assert.NoError(t, deny.RemoveAll("/work/missing"))
}

// Rename moves a whole subtree, and a move can carry a denied path out of the
// patterns' reach. Renaming an ancestor of "/work/secrets" would make its
// contents readable under the new name.
func TestDenyFS_RenameChecksTheSubtree(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		patterns []string
		oldname  string
		newname  string
		wantErr  bool
	}{
		"moving an ancestor out of an anchored pattern": {
			patterns: denyPatterns,
			oldname:  "/work",
			newname:  "/moved",
			wantErr:  true,
		},
		"moving a directory that holds a denied name": {
			patterns: denyPatterns,
			oldname:  "/work/sub",
			newname:  "/work/sub2",
			wantErr:  true,
		},
		"moving a directory with no denied entry below it": {
			patterns: denyPatterns,
			oldname:  "/work/clean",
			newname:  "/work/clean2",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			deny, base := seedSubtreeFS(t, tc.patterns)

			err := deny.Rename(tc.oldname, tc.newname)
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}

			require.Error(t, err)
			assert.ErrorIs(t, err, syscall.EACCES)

			// The move did not happen, so the denied paths keep their names.
			_, err = base.Stat(tc.oldname)
			assert.NoError(t, err)
			_, err = base.Stat("/work/secrets/db/pass.txt")
			assert.NoError(t, err)
		})
	}
}

// A denied entry stays visible in a directory listing. Only access to it is
// refused, which is what a file with no permissions looks like on a real
// system.
func TestDenyFS_ListingStillShowsDeniedEntries(t *testing.T) {
	t.Parallel()

	deny := seedDenyFS(t)

	entries, err := afero.ReadDir(deny, "/work")
	require.NoError(t, err)

	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.Contains(t, names, ".env")
	assert.Contains(t, names, "secrets")
}

// seedSymlinkDenyFS mounts a host directory, since a symlink is the point of these
// cases and MemMapFs has none.
func seedSymlinkDenyFS(t *testing.T, patterns []string) *vfs.DenyFS {
	t.Helper()

	hostDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(hostDir, "secrets/db"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(hostDir, ".env"), []byte("SECRET=1"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(hostDir, "main.go"), []byte("package main"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(hostDir, "secrets/db/pass.txt"), []byte("pw"), 0644))

	for name, target := range map[string]string{
		"env-link":     ".env",
		"secrets-link": "secrets",
		"main-link":    "main.go",
		"loop-a":       "loop-b",
		"loop-b":       "loop-a",
	} {
		require.NoError(t, os.Symlink(target, filepath.Join(hostDir, name)))
	}

	host, err := vfs.NewHostFS(hostDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = host.Close() })

	base := vfs.NewVFS(afero.NewMemMapFs())
	require.NoError(t, base.Mount("/work", host))

	deny, err := vfs.NewDenyFS(base, patterns)
	require.NoError(t, err)
	return deny
}

// A pattern selects paths, and a symlink is another path to the same file. Matching
// the name the caller typed is not enough: the name the operation ends up acting on
// has to be matched too.
func TestDenyFS_SymlinkToDeniedPath(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		patterns []string
		name     string
		wantDeny bool
	}{
		"a link to a denied file": {
			patterns: []string{"**/.env"},
			name:     "/work/env-link",
			wantDeny: true,
		},
		"a path below a link to a denied directory": {
			patterns: []string{"/work/secrets"},
			name:     "/work/secrets-link/db/pass.txt",
			wantDeny: true,
		},
		"the link to the denied directory itself": {
			patterns: []string{"/work/secrets"},
			name:     "/work/secrets-link",
			wantDeny: true,
		},
		"the denied path itself stays denied": {
			patterns: []string{"**/.env"},
			name:     "/work/.env",
			wantDeny: true,
		},
		"a link to a path no pattern selects": {
			patterns: []string{"**/.env"},
			name:     "/work/main-link",
		},
		"a path no pattern selects": {
			patterns: []string{"**/.env"},
			name:     "/work/main.go",
		},
		// Which file a loop reaches cannot be established, so the policy fails
		// closed rather than assuming it is none of the ones it protects.
		"a name that cannot be resolved is refused": {
			patterns: []string{"**/.env"},
			name:     "/work/loop-a",
			wantDeny: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			deny := seedSymlinkDenyFS(t, tc.patterns)

			_, readErr := afero.ReadFile(deny, tc.name)
			_, statErr := deny.Stat(tc.name)
			writeErr := afero.WriteFile(deny, tc.name, []byte("x"), 0644)

			if !tc.wantDeny {
				assert.NoError(t, readErr)
				assert.NoError(t, statErr)
				assert.NoError(t, writeErr)
				return
			}
			for op, err := range map[string]error{"read": readErr, "stat": statErr, "write": writeErr} {
				require.Error(t, err, op)
				assert.ErrorIs(t, err, syscall.EACCES, op)
			}
		})
	}
}

// Lstat goes through the same gate as Stat. A pattern selects a name, and lstat
// reports that name's own mode and kind, so leaving it ungated would answer for a
// path the policy refuses. Readlink is the other half of resolution and is not
// gated; TestDenyFS_ReadlinkIsNotGated states why.
func TestDenyFS_LstatIfPossible(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		patterns []string
		name     string
		wantDeny bool
	}{
		"a denied file": {
			patterns: []string{"**/.env"},
			name:     "/work/.env",
			wantDeny: true,
		},
		"a link whose target is denied": {
			patterns: []string{"**/.env"},
			name:     "/work/env-link",
			wantDeny: true,
		},
		"a file inside a denied directory": {
			patterns: []string{"/work/secrets"},
			name:     "/work/secrets/db/pass.txt",
			wantDeny: true,
		},
		"a file no pattern selects": {
			patterns: []string{"**/.env"},
			name:     "/work/main.go",
		},
		"a link no pattern selects": {
			patterns: []string{"**/.env"},
			name:     "/work/main-link",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			deny := seedSymlinkDenyFS(t, tc.patterns)

			fi, lstatUsed, err := deny.LstatIfPossible(tc.name)
			if tc.wantDeny {
				require.Error(t, err)
				assert.ErrorIs(t, err, syscall.EACCES)
				return
			}
			require.NoError(t, err)
			assert.True(t, lstatUsed)
			assert.NotNil(t, fi)
		})
	}
}

// Readlink is part of finding out which file a path names, which is what the deny
// layer needs before it can decide anything. Gating it would make resolution
// depend on the answer it is meant to produce.
func TestDenyFS_ReadlinkIsNotGated(t *testing.T) {
	t.Parallel()

	deny := seedSymlinkDenyFS(t, []string{"**/.env"})

	got, err := deny.ReadlinkIfPossible("/work/env-link")
	require.NoError(t, err)
	assert.Equal(t, ".env", got)
}
