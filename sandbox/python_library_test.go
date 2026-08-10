package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrtc0/sbsh/sandbox/python"
)

// stageLibrary writes a pure-Python package into a host directory, in the shape
// "pip install --target DIR greeting" leaves behind: the package itself plus the
// .dist-info metadata directory beside it. extra adds further files, relative to
// the directory, for the cases that stage something else.
func stageLibrary(t *testing.T, extra map[string]string) string {
	t.Helper()

	dir := t.TempDir()
	files := map[string]string{
		"greeting/__init__.py":              "from .core import hello\n",
		"greeting/core.py":                  "def hello(who):\n    return f'hello {who}'\n",
		"greeting-1.0.0.dist-info/METADATA": "Metadata-Version: 2.1\nName: greeting\nVersion: 1.0.0\n",
		"greeting-1.0.0.dist-info/RECORD":   "greeting/__init__.py,,\n",
	}
	for name, body := range extra {
		files[name] = body
	}
	for name, body := range files {
		p := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}
	return dir
}

// TestPythonLibrary covers the whole host workflow: a package tree prepared
// outside the sandbox, mounted at the conventional root, and imported by a
// script running inside it. Mounting is the entire arrangement — there is no
// option and no flag, which is what the convention buys.
func TestPythonLibrary(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		extra      map[string]string
		opts       func(hostDir string) []Option
		script     string
		wantStdout string
		wantStderr []string
		wantExit   int
	}{
		"a tree mounted at the conventional root is importable": {
			script:     `python -c "import greeting; print(greeting.hello('world'))"`,
			wantStdout: "hello world\n",
		},
		"a package is importable from a python script file too": {
			script: "echo \"import greeting; print(greeting.hello('file'))\" > /tmp/m.py\n" +
				"python /tmp/m.py\n",
			wantStdout: "hello file\n",
		},
		"a dependency of a staged package is importable": {
			// What "pip install --target" produces: the package asked for and
			// everything it needs, side by side in one tree.
			extra: map[string]string{
				"greeting/__init__.py": "import shouter\n\ndef hello(who):\n    return shouter.shout(f'hello {who}')\n",
				"shouter/__init__.py":  "def shout(s):\n    return s.upper()\n",
			},
			script:     `python -c "import greeting; print(greeting.hello('world'))"`,
			wantStdout: "HELLO WORLD\n",
		},
		"a tree mounted anywhere else is not importable": {
			opts: func(dir string) []Option {
				return []Option{WithHostMountRO(dir, "/opt/py")}
			},
			script:     `python -c "import greeting"`,
			wantStderr: []string{"No module named 'greeting'"},
			wantExit:   1,
		},
		"a sandbox with no library tree still runs python": {
			opts:       func(string) []Option { return nil },
			script:     `python -c "import json; print(json.dumps([1]))"`,
			wantStdout: "[1]\n",
		},
		"a read-only mount is read-only to the code importing from it": {
			script:   `echo pwned > /lib/python/site-packages/greeting/core.py`,
			wantExit: 1,
		},
		"a script cannot widen its own import path": {
			// PYTHONPATH is the interpreter's own bootstrap, set after the
			// script's environment, and the root does not come from it anyway.
			opts: func(dir string) []Option {
				return []Option{WithHostMountRO(dir, "/opt/py")}
			},
			script:     `export PYTHONPATH=/opt/py; python -c "import greeting"`,
			wantStderr: []string{"No module named 'greeting'"},
			wantExit:   1,
		},
		"a .pth file in the tree is processed": {
			extra: map[string]string{
				"vendored.pth":           "deep\n",
				"deep/extra/__init__.py": "def value():\n    return 'from a .pth path'\n",
			},
			script:     `python -c "import extra; print(extra.value())"`,
			wantStdout: "from a .pth path\n",
		},
		"a native extension fails as an ordinary Python import error": {
			// The runtime says nothing of its own about this. The interpreter's
			// report is what the user gets, the same as for any other import
			// that does not resolve.
			extra: map[string]string{
				"greeting/__init__.py":                               "from . import _speedups\n",
				"greeting/_speedups.cpython-314-x86_64-linux-gnu.so": "\x7fELF",
			},
			script: `python -c "import greeting"`,
			wantStderr: []string{
				"Traceback",
				"cannot import name '_speedups'",
			},
			wantExit: 1,
		},
		"a tree the deny patterns hide is skipped, not fatal": {
			// Declaring the root is not the same as proving it usable. The
			// sandbox still builds; the import is what fails.
			opts: func(dir string) []Option {
				return []Option{
					WithHostMountRO(dir, python.LibraryRoot),
					WithDenyPaths("/lib/**"),
				}
			},
			script:     `python -c "import greeting"`,
			wantStderr: []string{"No module named 'greeting'"},
			wantExit:   1,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := stageLibrary(t, tc.extra)
			opts := []Option{WithHostMountRO(dir, python.LibraryRoot)}
			if tc.opts != nil {
				opts = tc.opts(dir)
			}

			sb, err := New(context.Background(), opts...)
			require.NoError(t, err)
			t.Cleanup(func() { sb.Close() })

			res, err := sb.Exec(context.Background(), tc.script, nil)
			require.NoError(t, err)

			assert.Equal(t, tc.wantExit, res.ExitCode, "exit code; stderr=%q", res.Stderr)
			if tc.wantStdout != "" {
				assert.Equal(t, tc.wantStdout, res.Stdout)
			}
			for _, want := range tc.wantStderr {
				assert.Contains(t, res.Stderr, want)
			}
		})
	}
}

// TestPythonLibrary_Scoped pins that a library tree belongs to the sandbox it
// was mounted into. The root is baked into that sandbox's own copy of the
// standard library, so there is no host-global Python state to leak through.
func TestPythonLibrary_Scoped(t *testing.T) {
	t.Parallel()

	dir := stageLibrary(t, nil)

	with, err := New(context.Background(), WithHostMountRO(dir, python.LibraryRoot))
	require.NoError(t, err)
	t.Cleanup(func() { with.Close() })

	without, err := New(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { without.Close() })

	script := `python -c "import greeting; print(greeting.hello('x'))"`

	res, err := with.Exec(context.Background(), script, nil)
	require.NoError(t, err)
	assert.Equal(t, "hello x\n", res.Stdout)

	res, err = without.Exec(context.Background(), script, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, res.ExitCode)
	assert.Contains(t, res.Stderr, "No module named 'greeting'")
}
