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

// mountedFS builds the filesystem an interpreter runs against: the standard
// library at its usual place, with libraryRoots baked into the startup hook.
func mountedFS(t *testing.T, libraryRoots ...string) *vfs.VFS {
	t.Helper()

	mm, err := pywasm.MajorMinor()
	require.NoError(t, err)
	src, err := pywasm.Stdlib()
	require.NoError(t, err)
	stdlib, err := python.NewStdlibFS(src, libraryRoots...)
	require.NoError(t, err)

	fs := vfs.NewVFS(afero.NewMemMapFs())
	require.NoError(t, fs.Mount(python.LibPath(mm), stdlib))
	return fs
}

// mountLibrary stages a pure-Python package under root, in the shape a
// "pip install --target root greeting" leaves behind.
func mountLibrary(root string) func(t *testing.T, fs *vfs.VFS) {
	return func(t *testing.T, fs *vfs.VFS) {
		t.Helper()

		files := map[string]string{
			root + "/greeting/__init__.py":              "from .core import hello\n",
			root + "/greeting/core.py":                  "def hello(who):\n    return f'hello {who}'\n",
			root + "/greeting-1.0.0.dist-info/METADATA": "Metadata-Version: 2.1\nName: greeting\nVersion: 1.0.0\n",
			root + "/greeting-1.0.0.dist-info/RECORD":   "greeting/__init__.py,,\n",
		}
		for p, body := range files {
			require.NoError(t, afero.WriteFile(fs, p, []byte(body), 0o644))
		}
	}
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
		libPaths   []string
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
		"a library root is importable": {
			code:       "import greeting; print(greeting.hello('world'))",
			libPaths:   []string{python.LibraryRoot},
			setupFS:    mountLibrary(python.LibraryRoot),
			wantStdout: "hello world",
		},
		"library roots rank last, after the stdlib and site-packages": {
			// The startup hook runs at the end of site initialization and
			// site.addsitedir appends, so a staged package sits behind
			// everything the runtime provides and cannot shadow any of it.
			code: "import sys, os\n" +
				"lib = os.path.dirname(os.__file__)\n" +
				"print(sys.path.index(lib) < sys.path.index(" + strconv.Quote(sitePkg) + ") < sys.path.index(" + strconv.Quote(python.LibraryRoot) + "))\n",
			libPaths:   []string{python.LibraryRoot},
			setupFS:    mountLibrary(python.LibraryRoot),
			wantStdout: "True",
		},
		"roots keep the order they were given": {
			code:     "import sys; print(sys.path[-2:])",
			libPaths: []string{python.LibraryRoot, "/opt/py"},
			setupFS: func(t *testing.T, fs *vfs.VFS) {
				require.NoError(t, fs.MkdirAll(python.LibraryRoot, 0o755))
				require.NoError(t, fs.MkdirAll("/opt/py", 0o755))
			},
			wantStdout: "['" + python.LibraryRoot + "', '/opt/py']",
		},
		"a library root cannot shadow a stdlib module": {
			code:     "import json; print(json.dumps({'a': 1}))",
			libPaths: []string{python.LibraryRoot},
			setupFS: func(t *testing.T, fs *vfs.VFS) {
				mountLibrary(python.LibraryRoot)(t, fs)
				require.NoError(t, afero.WriteFile(fs, python.LibraryRoot+"/json.py", []byte("raise SystemExit('shadowed')\n"), 0o644))
			},
			wantStdout: `{"a": 1}`,
		},
		"a root that is not there is skipped rather than added": {
			// The conventional root is offered to every sandbox, so a startup
			// that insisted on it would break the ordinary case.
			code:       "import sys; print(" + strconv.Quote(python.LibraryRoot) + " in sys.path)",
			libPaths:   []string{python.LibraryRoot},
			wantStdout: "False",
		},
		"a root that is a file is skipped rather than raising at startup": {
			code:     "import sys; print(" + strconv.Quote(python.LibraryRoot) + " in sys.path)",
			libPaths: []string{python.LibraryRoot},
			setupFS: func(t *testing.T, fs *vfs.VFS) {
				require.NoError(t, fs.MkdirAll("/lib/python", 0o755))
				require.NoError(t, afero.WriteFile(fs, python.LibraryRoot, []byte("not a directory\n"), 0o644))
			},
			wantStdout: "False",
		},
		"a staged package is not importable when no root names it": {
			code:    "import greeting",
			setupFS: mountLibrary(python.LibraryRoot),
			wantErr: "ModuleNotFoundError: No module named 'greeting'",
		},
		"a root whose name needs escaping in the literal still works": {
			code:       "import greeting; print(greeting.hello('quoted'))",
			libPaths:   []string{`/lib/it's`},
			setupFS:    mountLibrary(`/lib/it's`),
			wantStdout: "hello quoted",
		},
		"a .pth file adds the path it names": {
			// site.addsitedir is what processes these, which is the whole
			// reason the root goes through it rather than sys.path.append.
			code:     "import extra; print(extra.value())",
			libPaths: []string{python.LibraryRoot},
			setupFS: func(t *testing.T, fs *vfs.VFS) {
				mountLibrary(python.LibraryRoot)(t, fs)
				require.NoError(t, afero.WriteFile(fs, python.LibraryRoot+"/deep/extra.py",
					[]byte("def value():\n    return 'from a .pth path'\n"), 0o644))
				require.NoError(t, afero.WriteFile(fs, python.LibraryRoot+"/vendored.pth", []byte("deep\n"), 0o644))
			},
			wantStdout: "from a .pth path",
		},
		"a .pth import line runs at startup": {
			// Part of the supported model, not an accident: a mounted tree is a
			// normal package location, and its code runs inside the same
			// sandbox as everything else.
			code:     "print('main')",
			libPaths: []string{python.LibraryRoot},
			setupFS: func(t *testing.T, fs *vfs.VFS) {
				require.NoError(t, fs.MkdirAll(python.LibraryRoot, 0o755))
				require.NoError(t, afero.WriteFile(fs, python.LibraryRoot+"/startup.pth",
					[]byte("import sys; sys.stdout.write('pth ran\\n')\n"), 0o644))
			},
			wantStdout: "pth ran\nmain",
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
			fs := mountedFS(t, tc.libPaths...)
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

// TestNewStdlibFS_RootLiteral covers the one thing the hook's generation refuses:
// a path it could not write into a Python literal. Everything else about a root
// is decided at runtime, by whether the import works.
func TestNewStdlibFS_RootLiteral(t *testing.T) {
	t.Parallel()

	src, err := pywasm.Stdlib()
	require.NoError(t, err)

	t.Run("a quote is escaped rather than refused", func(t *testing.T) {
		t.Parallel()

		_, err := python.NewStdlibFS(src, `/lib/it's/site-packages`)
		assert.NoError(t, err)
	})

	t.Run("a newline is refused", func(t *testing.T) {
		t.Parallel()

		_, err := python.NewStdlibFS(src, "/lib/\n/site-packages")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "non-printable character")
	})
}
