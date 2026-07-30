package vfs_test

import (
	"errors"
	"os"
	"syscall"
	"testing"

	"github.com/mrtc0/sbsh/vfs"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedFs(t *testing.T) afero.Fs {
	t.Helper()
	base := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(base, "/existing.txt", []byte("hello"), 0644))
	return base
}

func TestReadOnlyFS_ReadsSucceed(t *testing.T) {
	t.Parallel()

	ro := vfs.NewReadOnlyFS(seedFs(t))

	got, err := afero.ReadFile(ro, "/existing.txt")
	assert.NoError(t, err)
	assert.Equal(t, "hello", string(got))

	fi, err := ro.Stat("/existing.txt")
	assert.NoError(t, err)
	assert.Equal(t, int64(5), fi.Size())
}

func TestReadOnlyFS_WritesRejected(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		op func(ro afero.Fs) error
	}{
		"Create": {
			op: func(ro afero.Fs) error {
				_, err := ro.Create("/new.txt")
				return err
			},
		},
		"Mkdir": {
			op: func(ro afero.Fs) error { return ro.Mkdir("/dir", 0755) },
		},
		"MkdirAll": {
			op: func(ro afero.Fs) error { return ro.MkdirAll("/a/b", 0755) },
		},
		"Remove": {
			op: func(ro afero.Fs) error { return ro.Remove("/existing.txt") },
		},
		"RemoveAll": {
			op: func(ro afero.Fs) error { return ro.RemoveAll("/existing.txt") },
		},
		"Rename": {
			op: func(ro afero.Fs) error { return ro.Rename("/existing.txt", "/moved.txt") },
		},
		"Chmod": {
			op: func(ro afero.Fs) error { return ro.Chmod("/existing.txt", 0600) },
		},
		"Chown": {
			op: func(ro afero.Fs) error { return ro.Chown("/existing.txt", 0, 0) },
		},
		"OpenFile write flag": {
			op: func(ro afero.Fs) error {
				_, err := ro.OpenFile("/existing.txt", os.O_WRONLY, 0644)
				return err
			},
		},
		"OpenFile create flag": {
			op: func(ro afero.Fs) error {
				_, err := ro.OpenFile("/new.txt", os.O_RDONLY|os.O_CREATE, 0644)
				return err
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ro := vfs.NewReadOnlyFS(seedFs(t))

			err := tc.op(ro)
			require.Error(t, err)
			assert.ErrorIs(t, err, syscall.EROFS)

			got, readErr := afero.ReadFile(ro, "/existing.txt")
			require.NoError(t, readErr)
			assert.Equal(t, "hello", string(got))
		})
	}
}

func TestReadOnlyFS_FileHandleWriteRejected(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		op func(f afero.File) error
	}{
		"Write": {
			op: func(f afero.File) error {
				_, err := f.Write([]byte("x"))
				return err
			},
		},
		"WriteString": {
			op: func(f afero.File) error {
				_, err := f.WriteString("x")
				return err
			},
		},
		"Truncate": {
			op: func(f afero.File) error { return f.Truncate(0) },
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ro := vfs.NewReadOnlyFS(seedFs(t))

			f, err := ro.Open("/existing.txt")
			require.NoError(t, err)
			defer f.Close()

			assert.ErrorIs(t, tc.op(f), syscall.EROFS)
		})
	}
}

func TestReadOnlyFS_OverHostFS(t *testing.T) {
	t.Parallel()

	hostDir := t.TempDir()
	require.NoError(t, os.WriteFile(hostDir+"/data.txt", []byte("host"), 0644))

	hostfs, err := vfs.NewHostFS(hostDir)
	require.NoError(t, err)

	ro := vfs.NewReadOnlyFS(hostfs)

	got, err := afero.ReadFile(ro, "/data.txt")
	require.NoError(t, err)
	assert.Equal(t, "host", string(got))

	_, err = ro.Create("/evil.txt")
	assert.ErrorIs(t, err, syscall.EROFS)

	_, statErr := os.Stat(hostDir + "/evil.txt")
	assert.True(t, errors.Is(statErr, os.ErrNotExist))

	_, err = ro.OpenFile("/data.txt", os.O_WRONLY, 0644)
	assert.ErrorIs(t, err, syscall.EROFS)
}
