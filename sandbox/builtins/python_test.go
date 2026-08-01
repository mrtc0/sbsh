package builtins

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrtc0/sbsh/sandbox/python"
)

type fakePython struct{ got python.Invocation }

func (f *fakePython) Run(_ context.Context, inv python.Invocation) (python.InvocationResult, error) {
	f.got = inv
	return python.InvocationResult{ExitCode: 0}, nil
}

func TestPythonCommand_Invocation(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		dir      string
		args     []string
		seed     map[string]string
		wantCode string
		wantArgv []string
		wantCwd  string
	}{
		"-c mode passes code verbatim": {
			dir:      "/work",
			args:     []string{"-c", "print(1)", "a", "b"},
			wantCode: "print(1)",
			wantArgv: []string{"-c", "a", "b"},
			wantCwd:  "/work",
		},
		"file mode reads file and keeps argv0": {
			dir:      "/work",
			args:     []string{"main.py", "x"},
			seed:     map[string]string{"/work/main.py": "print('hi')\n"},
			wantCode: "print('hi')\n",
			wantArgv: []string{"main.py", "x"},
			wantCwd:  "/work",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			env, _, _ := NewTestEnv(t, tc.dir)
			fp := &fakePython{}
			env.Python = fp
			for path, body := range tc.seed {
				require.NoError(t, afero.WriteFile(env.FS, path, []byte(body), 0o644))
			}

			env.Args = tc.args
			require.NoError(t, pythonCommand(context.Background(), env))

			assert.Equal(t, tc.wantCode, fp.got.Code, "Code must be verbatim (no prelude)")
			assert.Equal(t, tc.wantArgv, fp.got.Argv)
			assert.Equal(t, tc.wantCwd, fp.got.Cwd)
		})
	}
}
