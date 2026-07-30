package vfs_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"

	"github.com/mrtc0/sbsh/vfs"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalize(t *testing.T) {
	t.Parallel()

	testCases := map[string]struct {
		input string
		want  string
	}{
		"simple": {
			input: "/foo/bar",
			want:  "/foo/bar",
		},
		"dot": {
			input: "/foo/./bar",
			want:  "/foo/bar",
		},
		"dot-dot": {
			input: "/foo/../bar",
			want:  "/bar",
		},
		"empty is the root": {
			input: "",
			want:  "/",
		},
		"root stays root": {
			input: "/",
			want:  "/",
		},
		"relative input is rooted": {
			input: "foo/bar",
			want:  "/foo/bar",
		},
		"trailing slash is dropped": {
			input: "/foo/bar/",
			want:  "/foo/bar",
		},
		"dot-dot cannot escape root": {
			input: "/../etc/passwd",
			want:  "/etc/passwd",
		},
		"repeated dot-dot is clamped to root": {
			input: "/../../../x",
			want:  "/x",
		},
		"relative dot-dot escape is clamped": {
			input: "a/../../b",
			want:  "/b",
		},
	}

	for n, tc := range testCases {
		t.Run(n, func(t *testing.T) {
			got := vfs.Normalize(tc.input)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestRel(t *testing.T) {
	t.Parallel()

	testCases := map[string]struct {
		base   string
		target string
		want   string
	}{
		"child of base": {
			base:   "/work/dir",
			target: "/work/dir/a",
			want:   "a",
		},
		"nested child": {
			base:   "/work/dir",
			target: "/work/dir/sub/b",
			want:   "sub/b",
		},
		"base equals target": {
			base:   "/work/dir",
			target: "/work/dir",
			want:   ".",
		},
		"root base strips leading slash": {
			base:   "/",
			target: "/foo/bar",
			want:   "foo/bar",
		},
		"root base of root": {
			base:   "/",
			target: "/",
			want:   ".",
		},
		"relative base is normalized": {
			base:   "work/dir",
			target: "/work/dir/a",
			want:   "a",
		},
		"trailing slash on base is normalized": {
			base:   "/work/dir/",
			target: "/work/dir/a",
			want:   "a",
		},
		"target not under base returns normalized target": {
			base:   "/work/dir",
			target: "/other/x",
			want:   "/other/x",
		},
		"sibling sharing a prefix is not treated as under base": {
			base:   "/work/dir",
			target: "/work/dirty/x",
			want:   "/work/dirty/x",
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, vfs.Rel(tc.base, tc.target))
		})
	}
}

func TestNewVFS(t *testing.T) {
	t.Parallel()

	tempdir := t.TempDir()
	root, err := vfs.NewHostFS(tempdir)
	assert.NoError(t, err)
	assert.NotNil(t, root)

	mountFS := vfs.NewVFS(root)
	assert.NotNil(t, mountFS)
}

func TestVFS_Mount(t *testing.T) {
	t.Parallel()

	fs := afero.NewMemMapFs()
	mountFS := vfs.NewVFS(fs)
	assert.NotNil(t, mountFS)

	mountDir := t.TempDir()
	mountRoot, err := vfs.NewHostFS(mountDir)
	assert.NoError(t, err)
	assert.NotNil(t, mountRoot)

	steps := []struct {
		name        string
		virtualPath string
		errStr      string
	}{
		{
			name:        "valid mount",
			virtualPath: "/mnt",
		},
		{
			name:        "mount over root",
			virtualPath: "/",
			errStr:      "cannot mount over root",
		},
		{
			name:        "duplicate mount",
			virtualPath: "/mnt",
			errStr:      "mount point \"/mnt\" already exists",
		},
	}

	for _, tc := range steps {
		t.Run(tc.name, func(t *testing.T) {
			err := mountFS.Mount(tc.virtualPath, mountRoot)
			if tc.errStr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errStr)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestVFS_ConcurrentMountAndResolve(t *testing.T) {
	t.Parallel()

	mountFS := vfs.NewVFS(afero.NewMemMapFs())

	var wg sync.WaitGroup

	// Concurrently mount filesystems and resolve paths to test thread safety.
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = mountFS.Mount(fmt.Sprintf("/mnt%d", i), afero.NewMemMapFs())
		}(i)
	}

	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_, _ = mountFS.Stat("/some/path")
			}
		}()
	}

	wg.Wait()
}

func TestVFS_LongestPrefixResolution(t *testing.T) {
	t.Parallel()

	root := afero.NewMemMapFs()
	fsA := afero.NewMemMapFs()
	fsB := afero.NewMemMapFs()

	m := vfs.NewVFS(root)
	require.NoError(t, m.Mount("/a", fsA))
	require.NoError(t, m.Mount("/a/b", fsB))

	testCases := map[string]struct {
		virtualPath  string
		wantFs       afero.Fs
		wantPathInFs string
	}{
		"root path -> root fs": {
			virtualPath:  "/x.txt",
			wantFs:       root,
			wantPathInFs: "/x.txt",
		},
		"under /a -> fsA": {
			virtualPath:  "/a/y.txt",
			wantFs:       fsA,
			wantPathInFs: "/y.txt",
		},
		"deeper /a -> fsA (not /a/b)": {
			virtualPath:  "/a/deep/z.txt",
			wantFs:       fsA,
			wantPathInFs: "/deep/z.txt",
		},
		"under /a/b -> fsB (longest match wins)": {
			virtualPath:  "/a/b/w.txt",
			wantFs:       fsB,
			wantPathInFs: "/w.txt",
		},
		"sibling with shared prefix -> root fs": {
			virtualPath:  "/ab.txt",
			wantFs:       root,
			wantPathInFs: "/ab.txt",
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			f, err := m.Create(tc.virtualPath)
			require.NoError(t, err)
			f.Close()

			ok, err := afero.Exists(tc.wantFs, tc.wantPathInFs)
			require.NoError(t, err)
			assert.True(t, ok, "expected file at %s in target fs", tc.wantPathInFs)

			for _, other := range []afero.Fs{root, fsA, fsB} {
				if other == tc.wantFs {
					continue
				}
				ok, err := afero.Exists(other, tc.wantPathInFs)
				require.NoError(t, err)
				assert.False(t, ok, "file must not leak into another fs at %s", tc.wantPathInFs)
			}
		})
	}
}

func TestVFS_ResolveRouting(t *testing.T) {
	t.Parallel()

	root := afero.NewMemMapFs()
	fsA := afero.NewMemMapFs()

	m := vfs.NewVFS(root)
	require.NoError(t, m.Mount("/a", fsA))

	testCases := map[string]struct {
		virtualPath  string
		wantFs       afero.Fs
		wantPathInFs string
	}{
		"escapes to root": {
			virtualPath:  "/a/../b.txt",
			wantFs:       root,
			wantPathInFs: "/b.txt",
		},
		"stays within mount": {
			virtualPath:  "/a/x/../y.txt",
			wantFs:       fsA,
			wantPathInFs: "/y.txt",
		},
		"single dot normalized": {
			virtualPath:  "/a/./z.txt",
			wantFs:       fsA,
			wantPathInFs: "/z.txt",
		},
		"clamped to root": {
			virtualPath:  "/a/../../../x.txt",
			wantFs:       root,
			wantPathInFs: "/x.txt",
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			f, err := m.Create(tc.virtualPath)
			require.NoError(t, err)
			f.Close()

			ok, err := afero.Exists(tc.wantFs, tc.wantPathInFs)
			require.NoError(t, err)
			assert.True(t, ok, "expected file at %s in target fs", tc.wantPathInFs)

			other := root
			if tc.wantFs == root {
				other = fsA
			}
			ok, err = afero.Exists(other, tc.wantPathInFs)
			require.NoError(t, err)
			assert.False(t, ok, "file must not leak into the other fs at %s", tc.wantPathInFs)
		})
	}
}

func TestVFS_RenameAcrossMounts(t *testing.T) {
	t.Parallel()

	fs1 := afero.NewMemMapFs()
	fs2 := afero.NewMemMapFs()

	m := vfs.NewVFS(afero.NewMemMapFs())
	require.NoError(t, m.Mount("/m1", fs1))
	require.NoError(t, m.Mount("/m2", fs2))

	f, err := m.Create("/m1/f.txt")
	require.NoError(t, err)
	f.Close()

	t.Run("across mounts rejected with EXDEV", func(t *testing.T) {
		err := m.Rename("/m1/f.txt", "/m2/f.txt")
		require.Error(t, err)
		assert.ErrorIs(t, err, syscall.EXDEV)

		ok, _ := afero.Exists(fs1, "/f.txt")
		assert.True(t, ok)
		ok, _ = afero.Exists(fs2, "/f.txt")
		assert.False(t, ok)
	})

	t.Run("within same mount succeeds", func(t *testing.T) {
		err := m.Rename("/m1/f.txt", "/m1/g.txt")
		require.NoError(t, err)

		ok, _ := afero.Exists(fs1, "/g.txt")
		assert.True(t, ok)
		ok, _ = afero.Exists(fs1, "/f.txt")
		assert.False(t, ok)
	})
}

func TestVFS_Create(t *testing.T) {
	t.Parallel()

	mountDir := t.TempDir()

	testCases := map[string]struct {
		virtualPath string
		fileName    string
		want        string
		errStr      string
	}{
		"create in root": {
			virtualPath: "/",
			fileName:    "testfile",
			want:        "testfile",
		},
		"create in mounted dir": {
			virtualPath: "/mnt",
			fileName:    "testfile",
			want:        "testfile",
		},
		"create in non-mounted dir": {
			virtualPath: "/nonexistent",
			fileName:    "testfile",
			want:        "/nonexistent/testfile",
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			mountFS := vfs.NewVFS(fs)
			assert.NotNil(t, mountFS)

			if tc.virtualPath != "/" {
				mountRoot, err := vfs.NewHostFS(mountDir)
				assert.NoError(t, err)
				assert.NotNil(t, mountRoot)

				err = mountFS.Mount(tc.virtualPath, mountRoot)
				assert.NoError(t, err)
			}

			f, err := mountFS.Create(tc.virtualPath + "/" + tc.fileName)
			if tc.errStr != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tc.errStr)
				return
			}
			assert.NoError(t, err)
			assert.NotNil(t, f)

			_, err = mountFS.Stat(tc.virtualPath + "/" + tc.fileName)
			assert.NoError(t, err)
		})
	}
}

// hostDirWithLinks returns a host directory holding one file and two links to it,
// one direct and one through a directory.
func hostDirWithLinks(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sub/plain.txt"), []byte("x"), 0644))
	require.NoError(t, os.Symlink("sub/plain.txt", filepath.Join(dir, "link")))
	require.NoError(t, os.Symlink("sub", filepath.Join(dir, "dirlink")))
	return dir
}

func TestVFS_EvalSymlinks(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		name string
		want string
	}{
		"a link inside a mount resolves and keeps the mount point": {
			name: "/work/link",
			want: "/work/sub/plain.txt",
		},
		"a path below a directory link": {
			name: "/work/dirlink/plain.txt",
			want: "/work/sub/plain.txt",
		},
		"the mount point itself": {
			name: "/work",
			want: "/work",
		},
		"a path with no link is itself": {
			name: "/work/sub/plain.txt",
			want: "/work/sub/plain.txt",
		},
		// The root filesystem is a MemMapFs, which reports neither of afero's
		// link interfaces, so it has no links to resolve.
		"a path outside every mount resolves to itself": {
			name: "/tmp/out.txt",
			want: "/tmp/out.txt",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			host, err := vfs.NewHostFS(hostDirWithLinks(t))
			require.NoError(t, err)
			t.Cleanup(func() { _ = host.Close() })

			v := vfs.NewVFS(afero.NewMemMapFs())
			require.NoError(t, v.Mount("/work", host))

			got, err := v.EvalSymlinks(tc.name)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// A mount can be wrapped in ReadOnlyFS, which must not stop resolution: read-only
// says nothing about which file a path names.
func TestReadOnlyFS_EvalSymlinksDelegates(t *testing.T) {
	t.Parallel()

	host, err := vfs.NewHostFS(hostDirWithLinks(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = host.Close() })

	v := vfs.NewVFS(afero.NewMemMapFs())
	require.NoError(t, v.Mount("/work", vfs.NewReadOnlyFS(host)))

	got, err := v.EvalSymlinks("/work/link")
	require.NoError(t, err)
	assert.Equal(t, "/work/sub/plain.txt", got)
}

// mountedLinkTree mounts hostDirWithLinks at /work, optionally read-only, and
// returns the router. The tree holds sub/plain.txt, a link to it, and dirlink, a
// link to the directory.
func mountedLinkTree(t *testing.T, readonly bool) *vfs.VFS {
	t.Helper()

	host, err := vfs.NewHostFS(hostDirWithLinks(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = host.Close() })

	var fsys afero.Fs = host
	if readonly {
		fsys = vfs.NewReadOnlyFS(host)
	}

	v := vfs.NewVFS(afero.NewMemMapFs())
	require.NoError(t, v.Mount("/work", fsys))
	return v
}

func TestVFS_LstatIfPossible(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		name         string
		readonly     bool
		wantLink     bool
		wantDir      bool
		wantLstatted bool
	}{
		"a link inside a mount": {
			name: "/work/link", wantLink: true, wantLstatted: true,
		},
		"a directory link inside a mount is not a directory": {
			name: "/work/dirlink", wantLink: true, wantLstatted: true,
		},
		"a plain file inside a mount": {
			name: "/work/sub/plain.txt", wantLstatted: true,
		},
		"the mount point itself": {
			name: "/work", wantDir: true, wantLstatted: true,
		},
		"a read-only mount still lstats": {
			name: "/work/dirlink", readonly: true, wantLink: true, wantLstatted: true,
		},
		// The root filesystem is a MemMapFs, which cannot hold a link, so there is
		// nothing for lstat to tell apart and Stat is the answer.
		"a path outside every mount falls back to Stat": {
			name: "/", wantDir: true, wantLstatted: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			v := mountedLinkTree(t, tc.readonly)

			fi, lstatUsed, err := v.LstatIfPossible(tc.name)
			require.NoError(t, err)
			assert.Equal(t, tc.wantLstatted, lstatUsed)
			assert.Equal(t, tc.wantLink, fi.Mode()&os.ModeSymlink != 0, "link bit")
			assert.Equal(t, tc.wantDir, fi.IsDir(), "directory bit")
		})
	}
}

func TestVFS_ReadlinkIfPossible(t *testing.T) {
	t.Parallel()

	v := mountedLinkTree(t, false)

	got, err := v.ReadlinkIfPossible("/work/dirlink")
	require.NoError(t, err)
	assert.Equal(t, "sub", got)

	_, err = v.ReadlinkIfPossible("/work/sub/plain.txt")
	assert.Error(t, err, "a plain file has no target")
}

// TestWalkDoesNotFollowDirectoryLinks is the invariant the lstat methods exist
// for. A recursive command decides where to go from what the walk reports, so a
// directory link answering as a directory is what carried rm -r and find into a
// tree they were not given. It is pinned here rather than per command because it
// holds for every caller of afero.Walk.
func TestWalkDoesNotFollowDirectoryLinks(t *testing.T) {
	t.Parallel()

	for _, readonly := range []bool{false, true} {
		t.Run(fmt.Sprintf("readonly=%v", readonly), func(t *testing.T) {
			t.Parallel()

			v := mountedLinkTree(t, readonly)

			var walked []string
			require.NoError(t, afero.Walk(v, "/work", func(p string, _ os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				walked = append(walked, p)
				return nil
			}))

			assert.Contains(t, walked, "/work/dirlink", "the link itself is an entry")
			assert.NotContains(t, walked, "/work/dirlink/plain.txt", "the walk must not enter the link")
			assert.Contains(t, walked, "/work/sub/plain.txt", "the real directory is still walked")
		})
	}
}
