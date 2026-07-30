package builtins

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/mrtc0/sh/v3/interp"
	"github.com/mrtc0/sh/v3/syntax"

	"github.com/mrtc0/sbsh/vfs"
)

func NewTestEnv(t *testing.T, dir string) (*Env, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()

	fs := vfs.NewVFS(afero.NewMemMapFs())
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	env := &Env{
		FS: fs,
		HC: interp.HandlerContext{
			Dir:    dir,
			Stdout: stdout,
			Stderr: stderr,
		},
	}
	return env, stdout, stderr
}

// NewTestEnvWithDeny builds an env whose filesystem enforces deny patterns. The
// base filesystem is returned as well, since seeding a path the patterns refuse
// has to bypass the deny layer, exactly as a host directory holding the file does.
func NewTestEnvWithDeny(t *testing.T, dir, cmd string, patterns ...string) (*Env, afero.Fs, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()

	base := vfs.NewVFS(afero.NewMemMapFs())
	deny, err := vfs.NewDenyFS(base, patterns)
	require.NoError(t, err)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	env := &Env{
		Name: cmd,
		FS:   deny,
		HC: interp.HandlerContext{
			Dir:    dir,
			Stdout: stdout,
			Stderr: stderr,
		},
	}
	return env, base, stdout, stderr
}

// NewTestEnvWithHostMount mounts a real directory at /work and enforces deny
// patterns on top of it. MemMapFs differs from a host filesystem where it
// matters here: it removes a non-empty directory in one step, so a test that
// needs the real ENOTEMPTY has to mount a host directory. The returned path is
// the host side, which a test seeds directly.
func NewTestEnvWithHostMount(t *testing.T, cmd string, patterns ...string) (*Env, string, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()

	hostDir := t.TempDir()
	host, err := vfs.NewHostFS(hostDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = host.Close() })

	base := vfs.NewVFS(afero.NewMemMapFs())
	require.NoError(t, base.Mount("/work", host))

	deny, err := vfs.NewDenyFS(base, patterns)
	require.NoError(t, err)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	env := &Env{
		Name: cmd,
		FS:   deny,
		HC: interp.HandlerContext{
			Dir:    "/work",
			Stdout: stdout,
			Stderr: stderr,
		},
	}
	return env, hostDir, stdout, stderr
}

func installTestCommands(t *testing.T) {
	t.Helper()

	saved := registry
	replacement := make(map[string]Func, len(saved)+6)
	for k, v := range saved {
		replacement[k] = v
	}
	registry = replacement
	t.Cleanup(func() { registry = saved })

	replacement["test_echo"] = func(_ context.Context, env *Env, args []string) error {
		fmt.Fprintln(env.HC.Stdout, strings.Join(args, " "))
		return nil
	}

	replacement["test_fail"] = func(_ context.Context, _ *Env, _ []string) error {
		return errors.New("boom")
	}

	replacement["test_exit"] = func(_ context.Context, _ *Env, _ []string) error {
		return exit(3)
	}

	replacement["test_exit_big"] = func(_ context.Context, _ *Env, _ []string) error {
		return exit(300)
	}

	replacement["test_exit_native"] = func(_ context.Context, _ *Env, _ []string) error {
		return interp.ExitStatus(3)
	}

	replacement["test_dir"] = func(_ context.Context, env *Env, _ []string) error {
		fmt.Fprintln(env.HC.Stdout, env.HC.Dir)
		return nil
	}

	replacement["test_cat"] = func(_ context.Context, env *Env, args []string) error {
		b, err := afero.ReadFile(env.FS, args[0])
		if err != nil {
			return err
		}
		env.HC.Stdout.Write(b)
		return nil
	}

	replacement["test_net"] = func(_ context.Context, env *Env, _ []string) error {
		if env.HTTP == nil {
			fmt.Fprintln(env.HC.Stdout, "nil")
			return nil
		}
		fmt.Fprintln(env.HC.Stdout, "client")
		return nil
	}
}

type runResult struct {
	stdout     string
	stderr     string
	exit       uint8
	nextCalled bool
}

// runScript runs the given script in a new interpreter with the given filesystem and options.
func runScript(t *testing.T, fs vfs.FS, opts Options, dir, script string) runResult {
	t.Helper()

	var res runResult
	var stdout, stderr bytes.Buffer

	spy := func(_ interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(_ context.Context, _ []string) error {
			res.nextCalled = true
			return interp.ExitStatus(0)
		}
	}

	runner, err := interp.New(
		interp.Dir(dir),
		interp.StdIO(nil, &stdout, &stderr),
		interp.ExecHandlers(ExecMiddleware(fs, opts), spy),
	)
	require.NoError(t, err, "interp.New")

	file, err := syntax.NewParser().Parse(strings.NewReader(script), "")
	require.NoErrorf(t, err, "parse %q", script)

	runErr := runner.Run(context.Background(), file)
	var es interp.ExitStatus
	switch {
	case runErr == nil:
		res.exit = 0
	case errors.As(runErr, &es):
		res.exit = uint8(es)
	default:
		t.Fatalf("unexpected run error: %v", runErr)
	}

	res.stdout = stdout.String()
	res.stderr = stderr.String()
	return res
}

func TestExecMiddleware(t *testing.T) {
	t.Parallel()
	installTestCommands(t)

	cases := map[string]struct {
		seed            map[string]string
		opts            Options
		script          string
		wantStdoutIsDir bool
		wantStdout      string
		wantStderr      string
		wantExit        uint8
		wantNextCalled  bool
	}{
		"runs a registered command and returns exit 0": {
			script:     "test_echo hello world",
			wantStdout: "hello world\n",
			wantExit:   0,
		},
		"unknown command returns 127 and command not found on stderr": {
			script:     "definitely_not_a_command",
			wantStderr: "definitely_not_a_command: command not found\n",
			wantExit:   127,
		},
		"a plain error becomes exit 1 with name-prefixed stderr": {
			script:     "test_fail",
			wantStderr: "test_fail: boom\n",
			wantExit:   1,
		},
		"a builtin exit error is translated to its code": {
			script:   "test_exit",
			wantExit: 3,
		},
		"an out-of-range exit code is reduced modulo 256": {
			script:   "test_exit_big",
			wantExit: 44, // 300 mod 256
		},
		"a native interp exit status is passed through unchanged": {
			script:   "test_exit_native",
			wantExit: 3,
		},
		"threads the runner Dir into Env.HC.Dir": {
			script:          "test_dir",
			wantStdoutIsDir: true,
		},
		"passes the given fsys to the command": {
			seed:       map[string]string{"/data.txt": "from-fs"},
			script:     "test_cat /data.txt",
			wantStdout: "from-fs",
		},
		"passes opts.HTTP through to Env": {
			opts:       Options{HTTP: &http.Client{}},
			script:     "test_net",
			wantStdout: "client\n",
		},
		"Env.HTTP is nil by default": {
			script:     "test_net",
			wantStdout: "nil\n",
		},
		"never spawns a real process even for a real binary path": {
			script:     "/bin/cat /etc/passwd",
			wantStderr: "/bin/cat: command not found\n",
			wantExit:   127,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fs := vfs.NewVFS(afero.NewMemMapFs())
			for path, body := range tc.seed {
				require.NoError(t, afero.WriteFile(fs, path, []byte(body), 0o644))
			}

			dir := t.TempDir()
			res := runScript(t, fs, tc.opts, dir, tc.script)

			assert.Equalf(t, tc.wantExit, res.exit, "exit code")
			if tc.wantStdoutIsDir {
				assert.Equal(t, dir, strings.TrimSpace(res.stdout), "stdout must carry the runner Dir")
			} else {
				assert.Equal(t, tc.wantStdout, res.stdout, "stdout")
			}
			assert.Equal(t, tc.wantStderr, res.stderr, "stderr")
			assert.Equal(t, tc.wantNextCalled, res.nextCalled, "next (real exec) must not be reached")
		})
	}
}
