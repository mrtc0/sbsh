package python

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	expsys "github.com/tetratelabs/wazero/experimental/sys"
)

func TestToErrno(t *testing.T) {
	t.Parallel()

	pathErr := func(err error) error {
		return &os.PathError{Op: "open", Path: "/f.txt", Err: err}
	}
	linkErr := func(err error) error {
		return &os.LinkError{Op: "rename", Old: "/a", New: "/b", Err: err}
	}

	cases := map[string]struct {
		err  error
		want expsys.Errno
	}{
		"nil is not an error": {err: nil, want: 0},
		"EROFS":               {err: pathErr(syscall.EROFS), want: expsys.EROFS},
		"EISDIR":              {err: pathErr(syscall.EISDIR), want: expsys.EISDIR},
		"ENOTDIR":             {err: pathErr(syscall.ENOTDIR), want: expsys.ENOTDIR},
		"EINVAL":              {err: pathErr(syscall.EINVAL), want: expsys.EINVAL},
		"ELOOP":               {err: pathErr(syscall.ELOOP), want: expsys.ELOOP},
		"ENAMETOOLONG":        {err: pathErr(syscall.ENAMETOOLONG), want: expsys.ENAMETOOLONG},
		"EACCES":              {err: pathErr(syscall.EACCES), want: expsys.EACCES},
		"read-only rename":    {err: linkErr(syscall.EROFS), want: expsys.EROFS},
		"fs.ErrNotExist":      {err: pathErr(fs.ErrNotExist), want: expsys.ENOENT},
		"fs.ErrExist":         {err: pathErr(fs.ErrExist), want: expsys.EEXIST},
		"fs.ErrPermission":    {err: pathErr(fs.ErrPermission), want: expsys.EACCES},
		"os.ErrClosed":        {err: pathErr(os.ErrClosed), want: expsys.EBADF},
		"unmapped error":      {err: errors.New("boom"), want: expsys.EIO},
		"unmapped syscall":    {err: pathErr(syscall.EAGAIN), want: expsys.EIO},

		// syscall.ENOTEMPTY satisfies errors.Is(err, fs.ErrExist), so matching the
		// sentinels first would report EEXIST here. Guards the case order.
		"ENOTEMPTY is not swallowed by fs.ErrExist": {
			err:  pathErr(syscall.ENOTEMPTY),
			want: expsys.ENOTEMPTY,
		},

		// WASI has no EXDEV; a cross-mount rename borrows EPERM.
		"EXDEV becomes EPERM": {err: linkErr(syscall.EXDEV), want: expsys.EPERM},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, toErrno(tc.err))
		})
	}
}
