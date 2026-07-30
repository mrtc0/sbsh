package vfs_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/mrtc0/sbsh/vfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewHostFS(t *testing.T) {
	t.Parallel()

	tmpdir := t.TempDir()
	hostfs, err := vfs.NewHostFS(tmpdir)
	assert.NoError(t, err)
	assert.NotNil(t, hostfs)
}

func TestHostFS_Create(t *testing.T) {
	t.Parallel()

	tmpdir := t.TempDir()
	hostfs, err := vfs.NewHostFS(tmpdir)
	assert.NoError(t, err)

	testCases := map[string]struct {
		name   string
		errStr string
	}{
		"relative path": {
			name: "testfile",
		},
		"empty file": {
			name:   "",
			errStr: ".: is a directory",
		},
		"absolute path": {
			name: "/testfile",
		},
		"parent dir": {
			name: "../testfile",
		},
	}

	for n, tc := range testCases {
		t.Run(n, func(t *testing.T) {
			f, err := hostfs.Create(tc.name)
			if tc.errStr != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tc.errStr)
				return
			}
			assert.NoError(t, err)
			assert.NotNil(t, f)

			_, err = hostfs.Stat(tc.name)
			assert.NoError(t, err)
		})
	}
}

func TestHostFS_SymlinkEscapeRejected(t *testing.T) {
	t.Parallel()

	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	require.NoError(t, os.WriteFile(secret, []byte("top-secret"), 0644))

	hostDir := t.TempDir()
	hostfs, err := vfs.NewHostFS(hostDir)
	require.NoError(t, err)

	testCases := map[string]struct {
		target string
	}{
		"absolute path outside root": {
			target: secret,
		},
		"relative path escaping root": {
			target: "../" + filepath.Base(outside) + "/secret.txt",
		},
		"symlink to parent dir": {
			target: "..",
		},
	}

	for n, tc := range testCases {
		t.Run(n, func(t *testing.T) {
			link := filepath.Join(hostDir, "link-"+n)
			require.NoError(t, os.Symlink(tc.target, link))

			f, err := hostfs.Open("/link-" + n)
			if f != nil {
				f.Close()
			}
			assert.Error(t, err, "symlink escape must be rejected")
		})
	}

	// In-root symlinks that resolve to a file inside the root should be allowed.
	inside := filepath.Join(hostDir, "inside.txt")
	require.NoError(t, os.WriteFile(inside, []byte("ok"), 0644))
	require.NoError(t, os.Symlink("inside.txt", filepath.Join(hostDir, "good-link")))

	f, err := hostfs.Open("/good-link")
	require.NoError(t, err, "in-root symlink must resolve")
	f.Close()
}

func TestHostFS_Mkdir(t *testing.T) {
	t.Parallel()

	tmpdir := t.TempDir()
	hostfs, err := vfs.NewHostFS(tmpdir)
	assert.NoError(t, err)

	testCases := map[string]struct {
		name   string
		errStr string
	}{
		"relative path": {
			name: "testdir-a",
		},
		"absolute path": {
			name: "/testdir-b",
		},
		"parent dir": {
			name: "../testdir-c",
		},
		"nested parent dir": {
			name: "testdir/../../testdir-d",
		},
		"empty path": {
			name:   "",
			errStr: ".: file exists",
		},
		"dot path": {
			name:   ".",
			errStr: ".: file exists",
		},
		"dot-dot path": {
			name:   "..",
			errStr: ".: file exists",
		},
		"slash path": {
			name:   "/",
			errStr: ".: file exists",
		},
	}

	for n, tc := range testCases {
		t.Run(n, func(t *testing.T) {
			err := hostfs.Mkdir(tc.name, 0755)
			if tc.errStr != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tc.errStr)
				return
			}
			assert.NoError(t, err)

			_, err = hostfs.Stat(tc.name)
			assert.NoError(t, err)
		})
	}
}

func TestHostFS_MkdirAll(t *testing.T) {
	t.Parallel()

	tmpdir := t.TempDir()
	hostfs, err := vfs.NewHostFS(tmpdir)
	assert.NoError(t, err)

	testCases := map[string]struct {
		name   string
		errStr string
	}{
		"relative path": {
			name: "testdir-a/testdir-b",
		},
		"absolute path": {
			name: "/testdir-c/testdir-d",
		},
		"parent dir": {
			name: "../testdir-e/testdir-f",
		},
		"dot path": {
			name: ".",
		},
	}

	for n, tc := range testCases {
		t.Run(n, func(t *testing.T) {
			err := hostfs.MkdirAll(tc.name, 0755)
			if tc.errStr != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tc.errStr)
				return
			}
			assert.NoError(t, err)

			_, err = hostfs.Stat(tc.name)
			assert.NoError(t, err)
		})
	}
}

// seedSymlinkTree lays out every shape resolution has to handle, so both tests
// below work from the same tree.
func seedSymlinkTree(t *testing.T) (*vfs.HostFS, string) {
	t.Helper()

	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("top-secret"), 0644))

	hostDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(hostDir, "dir/sub"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(hostDir, "plain.txt"), []byte("plain"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(hostDir, "dir/sub/deep.txt"), []byte("deep"), 0644))

	links := map[string]string{
		"link":     "plain.txt",        // one hop to a file
		"link2":    "link",             // a link to a link
		"dirlink":  "dir",              // a link to a directory
		"uplink":   "dir/../plain.txt", // a target that climbs back down
		"abslink":  filepath.Join(outside, "secret.txt"),
		"outlink":  "../" + filepath.Base(outside) + "/secret.txt",
		"loop-a":   "loop-b",
		"loop-b":   "loop-a",
		"dangling": "nowhere.txt",
	}
	for name, target := range links {
		require.NoError(t, os.Symlink(target, filepath.Join(hostDir, name)))
	}

	// A chain of nine links: os.Root follows eight, so this one is too deep.
	require.NoError(t, os.Symlink("plain.txt", filepath.Join(hostDir, "chain0")))
	for i := 1; i <= 9; i++ {
		require.NoError(t, os.Symlink(fmt.Sprintf("chain%d", i-1), filepath.Join(hostDir, fmt.Sprintf("chain%d", i))))
	}

	hostfs, err := vfs.NewHostFS(hostDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = hostfs.Close() })
	return hostfs, hostDir
}

func TestHostFS_EvalSymlinks(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		name    string
		want    string
		wantErr bool
	}{
		"a path with no link is itself":          {name: "/plain.txt", want: "/plain.txt"},
		"the root is itself":                     {name: "/", want: "/"},
		"a link to a file":                       {name: "/link", want: "/plain.txt"},
		"a link to a link":                       {name: "/link2", want: "/plain.txt"},
		"a link to a directory":                  {name: "/dirlink", want: "/dir"},
		"a path below a directory link":          {name: "/dirlink/sub/deep.txt", want: "/dir/sub/deep.txt"},
		"a target that climbs back down":         {name: "/uplink", want: "/plain.txt"},
		"a missing final component is kept":      {name: "/dir/new.txt", want: "/dir/new.txt"},
		"a missing name below a link resolves":   {name: "/dirlink/new.txt", want: "/dir/new.txt"},
		"a dangling link resolves to its target": {name: "/dangling", want: "/nowhere.txt"},
		"eight links resolve":                    {name: "/chain7", want: "/plain.txt"},
		"nine links are too deep":                {name: "/chain9", wantErr: true},
		"a loop is refused":                      {name: "/loop-a", wantErr: true},
		"an absolute target is refused":          {name: "/abslink", wantErr: true},
		"a target outside the root is refused":   {name: "/outlink", wantErr: true},
	}

	for n, tc := range tests {
		t.Run(n, func(t *testing.T) {
			t.Parallel()

			hostfs, _ := seedSymlinkTree(t)

			got, err := hostfs.EvalSymlinks(tc.name)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestHostFS_EvalSymlinksAgreesWithOpen pins resolution against os.Root itself.
// The deny policy matches the resolved name and the operation opens the original
// one, so the two agreeing is the whole point: if a Go release changed how os.Root
// treats a link, matching one path while opening another would be a hole.
func TestHostFS_EvalSymlinksAgreesWithOpen(t *testing.T) {
	t.Parallel()

	hostfs, _ := seedSymlinkTree(t)

	names := []string{
		"/plain.txt", "/link", "/link2", "/dirlink", "/dirlink/sub/deep.txt",
		"/uplink", "/chain7", "/chain9", "/loop-a", "/abslink", "/outlink",
		"/dangling", "/dir/new.txt", "/dirlink/new.txt",
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			resolved, resErr := hostfs.EvalSymlinks(name)

			f, openErr := hostfs.Open(name)
			if f != nil {
				t.Cleanup(func() { f.Close() })
			}

			switch {
			case openErr == nil:
				require.NoError(t, resErr, "open succeeded, so resolution must too")
				opened, err := f.Stat()
				require.NoError(t, err)
				viaResolved, err := hostfs.Stat(resolved)
				require.NoError(t, err, "the resolved path must name the same file")
				assert.True(t, os.SameFile(opened, viaResolved),
					"%s resolved to %s, which is a different file", name, resolved)
			case errors.Is(openErr, fs.ErrNotExist):
				// A path that does not exist resolves to itself: resolution is
				// also asked about paths that are about to be created.
				assert.NoError(t, resErr, "a missing path must resolve, not fail")
			default:
				assert.Error(t, resErr, "open failed with %v, so resolution must fail too", openErr)
			}
		})
	}
}

// TestHostFS_LstatIfPossible pins that lstat reports on the link and not on its
// target.
//
// Traversal and resolution ask different questions. afero.Walk decides whether to
// descend from the kind lstat reports, so a link that answers with its target's
// kind is what carries a recursive command into a tree it was not given. Stat
// answers about the target, which is correct for reading a file and wrong for
// deciding where a walk goes.
func TestHostFS_LstatIfPossible(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		name     string
		wantLink bool
		wantDir  bool
		wantErr  bool
	}{
		"a plain file is not a link":             {name: "/plain.txt"},
		"a directory is a directory":             {name: "/dir", wantDir: true},
		"a link to a file is a link":             {name: "/link", wantLink: true},
		"a link to a directory is not a dir":     {name: "/dirlink", wantLink: true},
		"a link to a link is a link":             {name: "/link2", wantLink: true},
		"a dangling link is still a link":        {name: "/dangling", wantLink: true},
		"a link with an absolute target":         {name: "/abslink", wantLink: true},
		"a link pointing outside the root":       {name: "/outlink", wantLink: true},
		"a link that loops":                      {name: "/loop-a", wantLink: true},
		"a chain deeper than the follow limit":   {name: "/chain9", wantLink: true},
		"a path that does not exist is an error": {name: "/nowhere.txt", wantErr: true},
	}

	for n, tc := range tests {
		t.Run(n, func(t *testing.T) {
			t.Parallel()

			hostfs, _ := seedSymlinkTree(t)

			fi, lstatUsed, err := hostfs.LstatIfPossible(tc.name)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.True(t, lstatUsed, "HostFS always lstats, so it must say so")
			assert.Equal(t, tc.wantLink, fi.Mode()&os.ModeSymlink != 0, "link bit")
			assert.Equal(t, tc.wantDir, fi.IsDir(), "directory bit")
		})
	}
}

// A link that Stat refuses to follow is still readable as a link, which is what
// lets a walk report the entry instead of ending at it.
func TestHostFS_LstatIfPossibleWhereStatFails(t *testing.T) {
	t.Parallel()

	hostfs, _ := seedSymlinkTree(t)

	for _, name := range []string{"/abslink", "/outlink", "/loop-a", "/chain9", "/dangling"} {
		t.Run(name, func(t *testing.T) {
			_, statErr := hostfs.Stat(name)
			require.Error(t, statErr, "the test case is pointless if Stat succeeds")

			fi, _, err := hostfs.LstatIfPossible(name)
			require.NoError(t, err)
			assert.NotZero(t, fi.Mode()&os.ModeSymlink)
		})
	}
}

func TestHostFS_ReadlinkIfPossible(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		name    string
		want    string
		wantErr bool
	}{
		"a link to a file":           {name: "/link", want: "plain.txt"},
		"a link to a directory":      {name: "/dirlink", want: "dir"},
		"a dangling link":            {name: "/dangling", want: "nowhere.txt"},
		"a plain file is an error":   {name: "/plain.txt", wantErr: true},
		"a missing path is an error": {name: "/nowhere.txt", wantErr: true},
	}

	for n, tc := range tests {
		t.Run(n, func(t *testing.T) {
			t.Parallel()

			hostfs, _ := seedSymlinkTree(t)

			got, err := hostfs.ReadlinkIfPossible(tc.name)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
