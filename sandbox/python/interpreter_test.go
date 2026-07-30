package python_test

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrtc0/sbsh/pywasm"
	"github.com/mrtc0/sbsh/sandbox/python"
	"github.com/mrtc0/sbsh/vfs"
)

func newInterp(t *testing.T) *python.WazeroInterpreter {
	t.Helper()

	mm, err := pywasm.MajorMinor()
	require.NoError(t, err)
	ip, err := python.NewWazeroInterpreter(context.Background(), python.Config{
		Wasm:       pywasm.Wasm,
		MajorMinor: mm,
	})
	require.NoError(t, err)
	t.Cleanup(func() { ip.Close(context.Background()) })
	return ip
}

func mountedFS(t *testing.T) *vfs.VFS {
	t.Helper()

	mm, err := pywasm.MajorMinor()
	require.NoError(t, err)
	src, err := pywasm.Stdlib()
	require.NoError(t, err)
	stdlib, err := python.NewStdlibFS(src)
	require.NoError(t, err)

	fs := vfs.NewVFS(afero.NewMemMapFs())
	require.NoError(t, fs.Mount(python.LibPath(mm), stdlib))
	return fs
}

func TestInterpreterRun(t *testing.T) {
	t.Parallel()

	mm, err := pywasm.MajorMinor()
	require.NoError(t, err)
	sitePkg := python.LibPath(mm) + "/site-packages"

	cases := map[string]struct {
		code       string
		argv       []string
		cwd        string
		setupFS    func(t *testing.T, fs *vfs.VFS)
		wantStdout string
		wantErr    string
	}{
		"site adds site-packages to sys.path": {
			code:       "import sys; print(" + strconv.Quote(sitePkg) + " in sys.path)",
			wantStdout: "True",
		},
		"site-packages ranks after stdlib": {
			code: "import sys, os\n" +
				"lib = os.path.dirname(os.__file__)\n" +
				"print(sys.path.index(lib) < sys.path.index(" + strconv.Quote(sitePkg) + "))\n",
			wantStdout: "True",
		},
		"argv in -c mode": {
			code:       "import sys; print(repr(sys.argv))",
			argv:       []string{"-c", "alpha", "beta"},
			wantStdout: "['-c', 'alpha', 'beta']",
		},
		"argv in script mode": {
			code:       "import sys; print(repr(sys.argv))",
			argv:       []string{"main.py", "one"},
			wantStdout: "['main.py', 'one']",
		},
		"cwd is applied": {
			code: "import os; print(os.getcwd())",
			cwd:  "/work/sub",
			setupFS: func(t *testing.T, fs *vfs.VFS) {
				require.NoError(t, fs.MkdirAll("/work/sub", 0o755))
			},
			wantStdout: "/work/sub",
		},
		"write to a read-only mount reports EROFS": {
			// EROFS does not surface as PermissionError: CPython reserves that for
			// EACCES/EPERM. Assert on the errno the interpreter actually observes.
			code: "import errno\n" +
				"try:\n" +
				"    open('/ro/f.txt', 'w')\n" +
				"except OSError as e:\n" +
				"    print(e.errno == errno.EROFS)\n",
			setupFS: func(t *testing.T, fs *vfs.VFS) {
				base := afero.NewMemMapFs()
				require.NoError(t, afero.WriteFile(base, "/f.txt", []byte("hi"), 0o644))
				require.NoError(t, fs.Mount("/ro", vfs.NewReadOnlyFS(base)))
			},
			wantStdout: "True",
		},
	}

	ip := newInterp(t)
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fs := mountedFS(t)
			if tc.setupFS != nil {
				tc.setupFS(t, fs)
			}
			res, err := ip.Run(context.Background(), python.Invocation{
				Code: tc.code,
				Argv: tc.argv,
				Cwd:  tc.cwd,
				FS:   fs,
			})
			require.NoError(t, err)

			if tc.wantErr != "" {
				assert.False(t, res.Ok(), "expected failure; stdout=%q", res.Stdout)
				assert.Contains(t, res.Stderr, tc.wantErr)
				return
			}
			require.True(t, res.Ok(), "run not ok; stderr=%q", res.Stderr)
			assert.Equal(t, tc.wantStdout, strings.TrimSpace(res.Stdout), "stderr=%q", res.Stderr)
		})
	}
}

// TestInterpreterRun_ExitCodeContract pins the contract that an error means the
// interpreter itself failed, while a program's termination — voluntary or forced
// by the sandbox — is carried by InvocationResult.ExitCode.
func TestInterpreterRun_ExitCodeContract(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		code     string
		ctx      func(t *testing.T) context.Context
		wantExit uint32
	}{
		"a program that runs to completion exits 0": {
			code:     "print('done')",
			wantExit: 0,
		},
		"a program calling sys.exit reports its own code": {
			code:     "import sys; sys.exit(3)",
			wantExit: 3,
		},
		"a program killed by the sandbox timeout exits 128+SIGKILL": {
			code: "while True: pass",
			ctx: func(t *testing.T) context.Context {
				ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
				t.Cleanup(cancel)
				return ctx
			},
			wantExit: 137,
		},
		"a program killed by cancellation exits 128+SIGINT": {
			code: "while True: pass",
			ctx: func(t *testing.T) context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			wantExit: 130,
		},
	}

	ip := newInterp(t)
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			if tc.ctx != nil {
				ctx = tc.ctx(t)
			}

			res, err := ip.Run(ctx, python.Invocation{Code: tc.code, FS: mountedFS(t)})
			require.NoError(t, err)
			assert.Equal(t, tc.wantExit, res.ExitCode, "exit code; stderr=%q", res.Stderr)
		})
	}
}
