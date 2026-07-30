package sandbox

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fsAssertion verifies the state of the sandbox filesystem after a script runs.
// It is used by cases whose behavior is a side effect on the VFS (redirects,
// mkdir, rm) rather than something observable through Result.
type fsAssertion func(t *testing.T, fs afero.Fs)

func TestSandbox_Exec(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		opts       []Option
		script     string
		stdin      io.Reader // nil means the script gets an empty standard input
		wantStdout string
		wantStderr string // asserted with Contains when non-empty
		wantExit   int
		wantErr    bool // a sandbox-level failure (parse / quarantine), distinct from a non-zero exit
		assertFS   fsAssertion
	}{
		{
			name:       "writes command output to stdout",
			script:     "echo hello",
			wantStdout: "hello\n",
		},
		{
			name:   "heredoc body is written to the redirected file",
			script: "cat <<EOF > out.txt\nhello\nworld\nEOF\n",
			assertFS: func(t *testing.T, fs afero.Fs) {
				b, err := afero.ReadFile(fs, "/out.txt")
				require.NoError(t, err)
				assert.Equal(t, "hello\nworld\n", string(b))
			},
		},
		{
			name:   "dash heredoc strips leading tabs before redirecting",
			script: "cat <<-EOF > out.txt\n\t\tindented\n\t\tlines\n\tEOF\n",
			assertFS: func(t *testing.T, fs afero.Fs) {
				b, err := afero.ReadFile(fs, "/out.txt")
				require.NoError(t, err)
				assert.Equal(t, "indented\nlines\n", string(b))
			},
		},
		{
			name:   "append redirect adds to an existing file",
			script: "echo one > out.txt\necho two >> out.txt\n",
			assertFS: func(t *testing.T, fs afero.Fs) {
				b, err := afero.ReadFile(fs, "/out.txt")
				require.NoError(t, err)
				assert.Equal(t, "one\ntwo\n", string(b))
			},
		},
		{
			name:       "unknown command reports not found and exits 127",
			script:     "nope",
			wantStderr: "command not found",
			wantExit:   127,
		},
		{
			name:     "a failing command sets a non-zero exit without a sandbox error",
			script:   "cat missing.txt",
			wantExit: 1,
		},
		{
			name:    "process substitution is rejected before running",
			script:  "cat <(echo hi)",
			wantErr: true,
		},
		{
			name:       "source finds a script through PATH in the virtual filesystem",
			script:     "echo 'echo sourced' > /tmp/lib.sh\nPATH=/tmp\nsource lib.sh\n",
			wantStdout: "sourced\n",
		},
		{
			name:       "the script reads the standard input it was given",
			script:     "cat",
			stdin:      strings.NewReader("hello-from-host\n"),
			wantStdout: "hello-from-host\n",
		},
		{
			name:       "a nil standard input reads as empty",
			script:     "cat",
			wantStdout: "",
		},
		{
			name:       "the standard input reaches a command inside a pipeline",
			script:     "cat | wc -c",
			stdin:      strings.NewReader("hello"),
			wantStdout: "       5\n",
		},
		{
			// End-to-end cover for the two things the build does to the artifacts:
			// zlib is linked in separately by the Docker build, and the module is
			// stripped of its debug sections afterwards. Either one going wrong
			// shows up here rather than as a puzzling import failure later.
			name:       "python imports the compiled-in extension modules",
			script:     `python -c "import zlib, json, re; print(zlib.crc32(b'x'), json.dumps({'a': 1}), re.sub('a', 'b', 'aaa'))"`,
			wantStdout: "2363233923 {\"a\": 1} bbb\n",
		},
		{
			// The exit code is the only thing that tells the caller the program was
			// killed rather than having failed on its own, so it must survive the
			// trip out of the interpreter. A real shell reports 128 + SIGKILL here.
			name:     "a python program killed by the sandbox timeout exits 128+SIGKILL",
			opts:     []Option{WithTimeout(200 * time.Millisecond)},
			script:   `python -c "while True: pass"`,
			wantExit: 137,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sb, err := New(context.Background(), tc.opts...)
			require.NoError(t, err)
			t.Cleanup(func() { sb.Close() })

			res, err := sb.Exec(context.Background(), tc.script, tc.stdin)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			assert.Equal(t, tc.wantExit, res.ExitCode, "exit code")
			assert.Equal(t, tc.wantStdout, res.Stdout, "stdout")
			if tc.wantStderr != "" {
				assert.Contains(t, res.Stderr, tc.wantStderr, "stderr")
			}
			if tc.assertFS != nil {
				tc.assertFS(t, sb.FS())
			}
		})
	}
}

// TestSandbox_ExecReportsBeingStoppedThroughTheExitCode pins which channel a
// script the sandbox stopped is reported through. Exec reserves its error for the
// sandbox failing; the timeout and the caller's cancellation are limits the caller
// asked for, so they arrive as an exit code of 128 + signal, the way a shell
// reports a killed process.
//
// The contexts are already finished when Exec is called, so neither case waits on
// the clock.
func TestSandbox_ExecReportsBeingStoppedThroughTheExitCode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		ctx      func(t *testing.T) context.Context
		wantExit int
	}{
		{
			name: "a cancelled context reports SIGINT",
			ctx: func(t *testing.T) context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			wantExit: 130,
		},
		{
			name: "an expired deadline reports SIGKILL",
			ctx: func(t *testing.T) context.Context {
				ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				t.Cleanup(cancel)
				return ctx
			},
			wantExit: 137,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sb, err := New(context.Background())
			require.NoError(t, err)
			t.Cleanup(func() { _ = sb.Close() })

			res, err := sb.Exec(tc.ctx(t), "echo hello", nil)
			require.NoError(t, err, "being stopped is not a sandbox failure")
			require.NotNil(t, res)
			assert.Equal(t, tc.wantExit, res.ExitCode)
		})
	}
}

// TestSandbox_ExecKeepsSessionState pins that a sandbox is one shell session:
// what a script changes about the shell itself is still in effect for the next
// one, the way it would be for someone typing at a prompt.
func TestSandbox_ExecKeepsSessionState(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		scripts    []string // all but the last set up state; the last is asserted on
		wantStdout string
	}{
		{
			name:       "the working directory survives the call that changed it",
			scripts:    []string{"cd /tmp", "pwd"},
			wantStdout: "/tmp\n",
		},
		{
			name:       "relative paths resolve against the persisted directory",
			scripts:    []string{"cd /tmp", "echo hi > f.txt", "cat /tmp/f.txt"},
			wantStdout: "hi\n",
		},
		{
			name:       "exported variables persist",
			scripts:    []string{"export FOO=bar", "echo $FOO"},
			wantStdout: "bar\n",
		},
		{
			name:       "shell variables persist",
			scripts:    []string{"FOO=bar", "echo $FOO"},
			wantStdout: "bar\n",
		},
		{
			name:       "functions persist",
			scripts:    []string{"greet() { echo hi; }", "greet"},
			wantStdout: "hi\n",
		},
		{
			name:       "exit does not end the session",
			scripts:    []string{"exit 3", "echo alive"},
			wantStdout: "alive\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sb, err := New(context.Background())
			require.NoError(t, err)
			t.Cleanup(func() { sb.Close() })

			var res *Result
			for _, script := range tc.scripts {
				res, err = sb.Exec(context.Background(), script, nil)
				require.NoErrorf(t, err, "running %q", script)
			}

			assert.Equal(t, tc.wantStdout, res.Stdout, "stdout of the last script")
		})
	}
}

// TestSandbox_HostPathsAreNotObservable pins the invariant that a script cannot
// learn anything about the host filesystem, not even whether a path exists.
//
// The asymmetry the cases rely on: /bin/sh and /etc/hosts exist on every host we
// build for (darwin and linux), but neither exists in the virtual filesystem.
// Any builtin that reports them is reaching past the handlers to the host.
func TestSandbox_HostPathsAreNotObservable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		script     string
		wantExit   int
		wantStderr string // asserted with Contains when non-empty
		denyStderr string // asserted with NotContains when non-empty
	}{
		{
			name:     "the virtual filesystem has no /bin",
			script:   "ls /bin",
			wantExit: 1,
		},
		{
			name:     "type -p does not report a path that exists only on the host",
			script:   "type -p /bin/sh",
			wantExit: 1,
		},
		{
			name:     "type -P does not report a path that exists only on the host",
			script:   "type -P /bin/sh",
			wantExit: 1,
		},
		{
			name:       "type does not report a path that exists only on the host",
			script:     "type /bin/sh",
			wantExit:   1,
			wantStderr: "not found",
		},
		{
			name:     "command -v does not report a path that exists only on the host",
			script:   "command -v /bin/sh",
			wantExit: 1,
		},
		{
			name:       "source does not read a file that exists only on the host",
			script:     "source /etc/hosts",
			wantExit:   1,
			denyStderr: "localhost",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sb, err := New(context.Background())
			require.NoError(t, err)
			t.Cleanup(func() { sb.Close() })

			res, err := sb.Exec(context.Background(), tc.script, nil)
			require.NoError(t, err)

			assert.Equal(t, tc.wantExit, res.ExitCode, "exit code")
			assert.Empty(t, res.Stdout, "stdout must not disclose host paths")
			if tc.wantStderr != "" {
				assert.Contains(t, res.Stderr, tc.wantStderr, "stderr")
			}
			if tc.denyStderr != "" {
				assert.NotContains(t, res.Stderr, tc.denyStderr, "stderr must not disclose host file contents")
			}
		})
	}
}

// TestSandbox_DenyPaths pins that the deny patterns hold on every path into the
// filesystem: the builtins, the shell's own redirects and globbing, and the
// Python bridge.
func TestSandbox_DenyPaths(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		denyPaths    []string
		script       string
		wantExit     int
		wantStdout   string // asserted with Contains when non-empty
		wantStderr   string // asserted with Contains when non-empty
		denyStdout   string // asserted with NotContains when non-empty
		wantUnedited bool   // the denied file still holds its original bytes
	}{
		{
			name:       "cat cannot read a denied file",
			denyPaths:  []string{"**/.env"},
			script:     "cat /work/.env",
			wantExit:   1,
			wantStderr: "permission denied",
			denyStdout: "SECRET",
		},
		{
			name:         "a redirect cannot write to a denied file",
			denyPaths:    []string{"**/.env"},
			script:       "echo overwritten > /work/.env",
			wantExit:     1,
			wantStderr:   "permission denied",
			wantUnedited: true,
		},
		{
			name:       "a glob that expands onto a denied file fails",
			denyPaths:  []string{"**/token.txt"},
			script:     "cat /work/*.txt",
			wantExit:   1,
			wantStderr: "permission denied",
			denyStdout: "TOKEN",
		},
		{
			name:       "denying a directory covers the files below it",
			denyPaths:  []string{"/work/secrets"},
			script:     "cat /work/secrets/db/pass.txt",
			wantExit:   1,
			wantStderr: "permission denied",
			denyStdout: "hunter2",
		},
		{
			name:       "Python cannot read a denied file either",
			denyPaths:  []string{"**/.env"},
			script:     `python -c "open('/work/.env').read()"`,
			wantExit:   1,
			wantStderr: "PermissionError",
			denyStdout: "SECRET",
		},
		{
			name:       "a denied file is still listed",
			denyPaths:  []string{"**/.env"},
			script:     "ls /work",
			wantStdout: ".env",
		},
		{
			name:       "paths the patterns do not select are untouched",
			denyPaths:  []string{"**/.env"},
			script:     "cat /work/main.go",
			wantStdout: "package main",
		},
		{
			name:       "a prefix of the pattern is not a match",
			denyPaths:  []string{"**/.env"},
			script:     "cat /work/.envrc",
			wantStdout: "export FOO=1",
		},
		// A denied entry costs its own place in the result, not the whole
		// command: the recursive builtins report it and cover the rest.
		{
			name:       "grep -r reaches the files it may read",
			denyPaths:  []string{"**/.env"},
			script:     "grep -r TOKEN /work",
			wantExit:   2,
			wantStdout: "token.txt:TOKEN=abc",
			wantStderr: "permission denied",
			denyStdout: "SECRET",
		},
		{
			name:       "find lists the entries it may stat",
			denyPaths:  []string{"**/.env"},
			script:     "find /work -type f",
			wantExit:   1,
			wantStdout: "/work/main.go",
			wantStderr: "permission denied",
			denyStdout: "/work/.env\n",
		},
		{
			name:       "cp -r copies the files it may read",
			denyPaths:  []string{"**/.env"},
			script:     "cp -r /work /tmp/copy; echo rc=$?; find /tmp/copy -type f",
			wantStdout: "rc=1",
			wantStderr: "permission denied",
			denyStdout: "/tmp/copy/.env\n",
		},
		{
			name:       "tar -c stores the members it may read",
			denyPaths:  []string{"**/.env"},
			script:     "tar -cf /tmp/x.tar -C /work .; echo rc=$?; tar -tf /tmp/x.tar",
			wantStdout: "rc=2",
			wantStderr: "permission denied",
			denyStdout: "./.env\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			work := afero.NewMemMapFs()
			require.NoError(t, afero.WriteFile(work, "/.env", []byte("SECRET=1\n"), 0644))
			require.NoError(t, afero.WriteFile(work, "/.envrc", []byte("export FOO=1\n"), 0644))
			require.NoError(t, afero.WriteFile(work, "/main.go", []byte("package main\n"), 0644))
			require.NoError(t, afero.WriteFile(work, "/token.txt", []byte("TOKEN=abc\n"), 0644))
			require.NoError(t, afero.WriteFile(work, "/secrets/db/pass.txt", []byte("hunter2\n"), 0644))

			sb, err := New(context.Background(),
				WithMountRW("/work", work),
				WithDenyPaths(tc.denyPaths...),
			)
			require.NoError(t, err)
			t.Cleanup(func() { sb.Close() })

			res, err := sb.Exec(context.Background(), tc.script, nil)
			require.NoError(t, err)

			assert.Equal(t, tc.wantExit, res.ExitCode, "exit code (stderr: %s)", res.Stderr)
			if tc.wantStdout != "" {
				assert.Contains(t, res.Stdout, tc.wantStdout, "stdout")
			}
			if tc.wantStderr != "" {
				assert.Contains(t, res.Stderr, tc.wantStderr, "stderr")
			}
			if tc.denyStdout != "" {
				assert.NotContains(t, res.Stdout, tc.denyStdout, "stdout must not disclose a denied file")
			}
			if tc.wantUnedited {
				// Read through the mount source, not the sandbox: the point is
				// that the bytes on the other side of the deny layer are intact.
				b, readErr := afero.ReadFile(work, "/.env")
				require.NoError(t, readErr)
				assert.Equal(t, "SECRET=1\n", string(b))
			}
		})
	}
}

// TestSandbox_DenyPathsOnAHostMount pins the cases a MemMapFs mount cannot show.
// os.Root refuses to remove a directory that still holds a refused entry, and a
// rename moves a whole subtree at once, so an anchored pattern would stop
// selecting what it was written for.
func TestSandbox_DenyPathsOnAHostMount(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		denyPaths  []string
		script     string
		wantExit   int
		wantStdout string
		wantStderr string
		denyStdout string
		wantKept   []string
		wantGone   []string
	}{
		{
			name:       "rm -r removes what it may and keeps the rest",
			denyPaths:  []string{"**/.env"},
			script:     "rm -rf /work/sub",
			wantExit:   1,
			wantStderr: "permission denied",
			wantKept:   []string{"sub", "sub/.env"},
			wantGone:   []string{"sub/a.txt"},
		},
		{
			name:       "moving an ancestor of a denied directory is refused",
			denyPaths:  []string{"/work/a/secrets"},
			script:     "mv /work/a /work/b; echo rc=$?; cat /work/b/secrets/pass.txt",
			wantExit:   1,
			wantStdout: "rc=1",
			wantStderr: "permission denied",
			denyStdout: "hunter2",
			wantKept:   []string{"a/secrets/pass.txt"},
		},
		{
			name:       "a symlink to a denied file is refused",
			denyPaths:  []string{"**/.env"},
			script:     "cat /work/sub/env-link",
			wantExit:   1,
			wantStderr: "permission denied",
			denyStdout: "SECRET",
		},
		{
			name:       "python cannot read a denied file through a symlink either",
			denyPaths:  []string{"**/.env"},
			script:     `python -c "open('/work/sub/env-link').read()"`,
			wantExit:   1,
			wantStderr: "PermissionError",
			denyStdout: "SECRET",
		},
		{
			name:       "a symlink to a path no pattern selects still works",
			denyPaths:  []string{"**/.env"},
			script:     "cat /work/sub/a-link",
			wantStdout: "alpha",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			hostDir := t.TempDir()
			for rel, body := range map[string]string{
				"sub/.env":           "SECRET=1\n",
				"sub/a.txt":          "alpha\n",
				"a/secrets/pass.txt": "hunter2\n",
			} {
				full := filepath.Join(hostDir, rel)
				require.NoError(t, os.MkdirAll(filepath.Dir(full), 0755))
				require.NoError(t, os.WriteFile(full, []byte(body), 0644))
			}
			// Links that already exist in the mount source: the sandbox has no way
			// to create one, so this is the shape the risk actually takes.
			require.NoError(t, os.Symlink(".env", filepath.Join(hostDir, "sub/env-link")))
			require.NoError(t, os.Symlink("a.txt", filepath.Join(hostDir, "sub/a-link")))

			sb, err := New(context.Background(),
				WithHostMountRW(hostDir, "/work"),
				WithDenyPaths(tc.denyPaths...),
			)
			require.NoError(t, err)
			t.Cleanup(func() { sb.Close() })

			res, err := sb.Exec(context.Background(), tc.script, nil)
			require.NoError(t, err)

			assert.Equal(t, tc.wantExit, res.ExitCode, "exit code (stderr: %s)", res.Stderr)
			if tc.wantStdout != "" {
				assert.Contains(t, res.Stdout, tc.wantStdout, "stdout")
			}
			if tc.wantStderr != "" {
				assert.Contains(t, res.Stderr, tc.wantStderr, "stderr")
			}
			if tc.denyStdout != "" {
				assert.NotContains(t, res.Stdout, tc.denyStdout, "stdout must not disclose a denied file")
			}
			for _, rel := range tc.wantKept {
				_, statErr := os.Stat(filepath.Join(hostDir, rel))
				assert.NoError(t, statErr, "%s should be kept", rel)
			}
			for _, rel := range tc.wantGone {
				_, statErr := os.Stat(filepath.Join(hostDir, rel))
				assert.True(t, os.IsNotExist(statErr), "%s should be gone", rel)
			}
		})
	}
}

// TestSandbox_RecursiveCommandsStayInsideTheirArgument pins the guarantee a
// recursive command makes about its argument: it acts on what is below the path it
// was given, and a symbolic link is an entry there rather than a way out of it.
//
// The script is what an agent writes. The assertions are on the host side, because
// the damage a followed link does is to files the script never named.
func TestSandbox_RecursiveCommandsStayInsideTheirArgument(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		script     string
		wantExit   int
		wantStdout string
		wantKept   []string
		wantGone   []string
	}{
		{
			name:     "rm -r removes the link and leaves its target",
			script:   "rm -r /work/build",
			wantKept: []string{"data/keep.txt"},
			wantGone: []string{"build", "build/out.txt", "build/link"},
		},
		{
			name:       "find reports the link without reading through it",
			script:     "find /work/build",
			wantStdout: "/work/build/link\n",
			wantKept:   []string{"data/keep.txt", "build/link"},
		},
		{
			name:       "grep -r does not report a file twice through a link",
			script:     "grep -rc keep /work",
			wantStdout: "/work/data/keep.txt:1\n",
			wantKept:   []string{"data/keep.txt"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			hostDir := t.TempDir()
			for rel, body := range map[string]string{
				"data/keep.txt": "keep me\n",
				"build/out.txt": "artifact\n",
			} {
				full := filepath.Join(hostDir, rel)
				require.NoError(t, os.MkdirAll(filepath.Dir(full), 0755))
				require.NoError(t, os.WriteFile(full, []byte(body), 0644))
			}
			// A link to a sibling directory, the shape a build tree or a virtualenv
			// leaves behind. The sandbox cannot create one, so it comes from the
			// mount source.
			require.NoError(t, os.Symlink("../data", filepath.Join(hostDir, "build/link")))

			sb, err := New(context.Background(), WithHostMountRW(hostDir, "/work"))
			require.NoError(t, err)
			t.Cleanup(func() { sb.Close() })

			res, err := sb.Exec(context.Background(), tc.script, nil)
			require.NoError(t, err)

			assert.Equal(t, tc.wantExit, res.ExitCode, "exit code (stderr: %s)", res.Stderr)
			if tc.wantStdout != "" {
				assert.Contains(t, res.Stdout, tc.wantStdout, "stdout")
			}
			for _, rel := range tc.wantKept {
				_, statErr := os.Lstat(filepath.Join(hostDir, rel))
				assert.NoError(t, statErr, "%s should be kept", rel)
			}
			for _, rel := range tc.wantGone {
				_, statErr := os.Lstat(filepath.Join(hostDir, rel))
				assert.True(t, os.IsNotExist(statErr), "%s should be gone", rel)
			}
		})
	}
}

func TestSandbox_NetworkPolicy(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		allow      []string
		wantStdout string
		wantStderr string
		wantExit   int
	}{
		{
			name:       "without the option there is no network at all",
			wantStderr: "network access is not permitted",
			wantExit:   1,
		},
		{
			name:       "an entry covering the destination lets the request through",
			allow:      []string{"127.0.0.1/32"},
			wantStdout: "reached",
		},
		{
			name:       "an entry for something else does not",
			allow:      []string{"example.com"},
			wantStderr: "network policy",
			wantExit:   1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				io.WriteString(w, "reached")
			}))
			t.Cleanup(srv.Close)

			sb, err := New(context.Background(), WithNetworkAllow(tc.allow...))
			require.NoError(t, err)
			t.Cleanup(func() { sb.Close() })

			res, err := sb.Exec(context.Background(), "curl "+srv.URL, nil)
			require.NoError(t, err)

			assert.Equal(t, tc.wantExit, res.ExitCode, "exit code (stderr: %s)", res.Stderr)
			assert.Equal(t, tc.wantStdout, res.Stdout, "stdout")
			if tc.wantStderr != "" {
				assert.Contains(t, res.Stderr, tc.wantStderr, "stderr")
			}
		})
	}
}

func TestNew_RejectsInvalidNetworkEntry(t *testing.T) {
	t.Parallel()

	_, err := New(context.Background(), WithNetworkAllow("*"))
	assert.Error(t, err)
}

func TestNew_RejectsInvalidDenyPath(t *testing.T) {
	t.Parallel()

	_, err := New(context.Background(), WithDenyPaths("[bad"))
	assert.Error(t, err)
}

func TestNew_initFS(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		opts   []Option
		exist  []string
		absent []string
	}{
		{
			name:  "default dirs always exist",
			exist: []string{"/home/agent", "/tmp"},
		},
		{
			name:  "read-only mount creates parents at construction",
			opts:  []Option{WithMountRO("/opt/foo/bar", afero.NewMemMapFs())},
			exist: []string{"/opt", "/opt/foo", "/opt/foo/bar"},
		},
		{
			name:  "python stdlib is always mounted",
			exist: []string{"/usr", "/usr/lib"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sb, err := New(context.Background(), tc.opts...)
			require.NoError(t, err)
			t.Cleanup(func() { sb.Close() })

			for _, p := range tc.exist {
				info, err := sb.FS().Stat(p)
				require.NoErrorf(t, err, "Stat(%q) should succeed from construction", p)
				assert.Truef(t, info.IsDir(), "%q should be a directory", p)
			}
			for _, p := range tc.absent {
				_, err := sb.FS().Stat(p)
				assert.Errorf(t, err, "%q should not exist", p)
			}
		})
	}
}
