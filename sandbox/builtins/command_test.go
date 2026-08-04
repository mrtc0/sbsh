package builtins

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/mrtc0/sh/v3/expand"
	"github.com/mrtc0/sh/v3/interp"
	"github.com/mrtc0/sh/v3/syntax"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrtc0/sbsh/vfs"

	"github.com/mrtc0/sbsh/sandbox/command"
)

func NewTestEnv(t *testing.T, dir string) (*command.Invocation, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()

	fs := vfs.NewVFS(afero.NewMemMapFs())
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	inv := &command.Invocation{
		FS:     fs,
		Dir:    dir,
		Stdout: stdout,
		Stderr: stderr,
		Env:    NewEnviron(expand.ListEnviron()),
	}
	return inv, stdout, stderr
}

// NewTestEnvWithDeny builds an invocation whose filesystem enforces deny
// patterns. The base filesystem is returned as well, since seeding a path the
// patterns refuse has to bypass the deny layer, exactly as a host directory
// holding the file does.
func NewTestEnvWithDeny(t *testing.T, dir, cmd string, patterns ...string) (*command.Invocation, afero.Fs, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()

	base := vfs.NewVFS(afero.NewMemMapFs())
	deny, err := vfs.NewDenyFS(base, patterns)
	require.NoError(t, err)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	inv := &command.Invocation{
		Name:   cmd,
		FS:     deny,
		Dir:    dir,
		Stdout: stdout,
		Stderr: stderr,
		Env:    NewEnviron(expand.ListEnviron()),
	}
	return inv, base, stdout, stderr
}

// NewTestEnvWithHostMount mounts a real directory at /work and enforces deny
// patterns on top of it. MemMapFs differs from a host filesystem where it
// matters here: it removes a non-empty directory in one step, so a test that
// needs the real ENOTEMPTY has to mount a host directory. The returned path is
// the host side, which a test seeds directly.
func NewTestEnvWithHostMount(t *testing.T, cmd string, patterns ...string) (*command.Invocation, string, *bytes.Buffer, *bytes.Buffer) {
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
	inv := &command.Invocation{
		Name:   cmd,
		FS:     deny,
		Dir:    "/work",
		Stdout: stdout,
		Stderr: stderr,
		Env:    NewEnviron(expand.ListEnviron()),
	}
	return inv, hostDir, stdout, stderr
}

func installTestCommands(t *testing.T) {
	t.Helper()

	saved := registry
	replacement := make(map[string]command.RunFunc, len(saved)+6)
	for k, v := range saved {
		replacement[k] = v
	}
	registry = replacement
	t.Cleanup(func() { registry = saved })

	replacement["test_echo"] = func(_ context.Context, inv *command.Invocation) error {
		fmt.Fprintln(inv.Stdout, strings.Join(inv.Args, " "))
		return nil
	}

	replacement["test_fail"] = func(_ context.Context, _ *command.Invocation) error {
		return errors.New("boom")
	}

	replacement["test_exit"] = func(_ context.Context, _ *command.Invocation) error {
		return command.Exit(3)
	}

	replacement["test_exit_big"] = func(_ context.Context, _ *command.Invocation) error {
		return command.Exit(300)
	}

	replacement["test_exit_msg"] = func(_ context.Context, _ *command.Invocation) error {
		return command.Exit(2, "bad usage")
	}

	replacement["test_exit_wrapped"] = func(_ context.Context, _ *command.Invocation) error {
		return fmt.Errorf("wrapped: %w", command.Exit(2, "bad usage"))
	}

	replacement["test_exit_wrapped_silent"] = func(_ context.Context, _ *command.Invocation) error {
		return fmt.Errorf("wrapped: %w", command.Exit(3))
	}

	replacement["test_exit_native"] = func(_ context.Context, _ *command.Invocation) error {
		return interp.ExitStatus(3)
	}

	replacement["test_dir"] = func(_ context.Context, inv *command.Invocation) error {
		fmt.Fprintln(inv.Stdout, inv.Dir)
		return nil
	}

	replacement["test_cat"] = func(_ context.Context, inv *command.Invocation) error {
		b, err := afero.ReadFile(inv.FS, inv.Args[0])
		if err != nil {
			return err
		}
		inv.Stdout.Write(b)
		return nil
	}

	replacement["test_net"] = func(_ context.Context, inv *command.Invocation) error {
		if inv.HTTP == nil {
			fmt.Fprintln(inv.Stdout, "nil")
			return nil
		}
		fmt.Fprintln(inv.Stdout, "client")
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
		// Returning a plain error is outside the command contract; the fallback
		// is there so a misuse still reports something rather than passing for
		// success.
		"a plain error becomes exit 1 with name-prefixed stderr": {
			script:     "test_fail",
			wantStderr: "test_fail: boom\n",
			wantExit:   1,
		},
		"an exit without a message is silent": {
			script:   "test_exit",
			wantExit: 3,
		},
		"an exit with a message prints name: message": {
			script:     "test_exit_msg",
			wantStderr: "test_exit_msg: bad usage\n",
			wantExit:   2,
		},
		"a wrapped exit prints its own message, not the wrapper's": {
			script:     "test_exit_wrapped",
			wantStderr: "test_exit_wrapped: bad usage\n",
			wantExit:   2,
		},
		"a wrapped message-less exit stays silent": {
			script:   "test_exit_wrapped_silent",
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
		"threads the runner Dir into Env.Dir": {
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

func TestShellEnviron(t *testing.T) {
	t.Parallel()

	// EMPTY is set to the empty string, so Lookup must report ok for it while
	// reporting !ok for a name that was never set: the two are different to a
	// command that treats "set at all" as the signal.
	inv := NewEnviron(expand.ListEnviron("NAME=value", "EMPTY="))

	value, ok := inv.Lookup("NAME")
	assert.True(t, ok)
	assert.Equal(t, "value", value)

	value, ok = inv.Lookup("EMPTY")
	assert.True(t, ok, "a variable set to the empty string is still set")
	assert.Equal(t, "", value)

	value, ok = inv.Lookup("MISSING")
	assert.False(t, ok)
	assert.Equal(t, "", value)

	assert.ElementsMatch(t, []string{"NAME=value", "EMPTY="}, inv.All())
}

// arrayEnviron reports ARR as an array variable, which has no single value to
// render, alongside a plain string.
type arrayEnviron struct{ expand.Environ }

func (arrayEnviron) Get(name string) expand.Variable {
	switch name {
	case "ARR":
		return expand.Variable{Set: true, Kind: expand.Indexed, List: []string{"a", "b"}}
	case "STR":
		return expand.Variable{Set: true, Kind: expand.String, Str: "s"}
	}
	return expand.Variable{}
}

func (a arrayEnviron) Each(fn func(name string, vr expand.Variable) bool) {
	for _, name := range []string{"ARR", "STR"} {
		if !fn(name, a.Get(name)) {
			return
		}
	}
}

func TestShellEnviron_ExcludesArrays(t *testing.T) {
	t.Parallel()

	inv := NewEnviron(arrayEnviron{expand.ListEnviron()})

	assert.Equal(t, []string{"STR=s"}, inv.All())

	_, ok := inv.Lookup("ARR")
	assert.False(t, ok, "an array has no single value to return")
}
