package builtins

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func buildLines(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteByte(byte('a' + i))
		b.WriteByte('\n')
	}
	return b.String()
}

func Test_tail(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		seed         string
		stdin        string
		args         []string
		wantStdout   string
		wantNewlines int
	}{
		"prints the last n lines": {
			seed:       "a\nb\nc\nd\n",
			args:       []string{"-n", "2", "f"},
			wantStdout: "c\nd\n",
		},
		"prints everything when n exceeds the line count": {
			seed:       "a\nb\n",
			args:       []string{"-n", "100", "f"},
			wantStdout: "a\nb\n",
		},
		"negative -n prints the last K lines": {
			seed:       "a\nb\nc\nd\n",
			args:       []string{"-n", "-2", "f"},
			wantStdout: "c\nd\n",
		},
		"reads stdin when no file is given": {
			stdin:      "x\ny\nz\n",
			args:       []string{"-n", "1"},
			wantStdout: "z\n",
		},
		"defaults to 10 lines": {
			seed:         buildLines(15),
			args:         []string{"f"},
			wantNewlines: 10,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			env, stdout, _ := NewTestEnv(t, "/work")
			if tc.seed != "" {
				mustWrite(t, env.FS, "/work/f", tc.seed)
			}
			if tc.stdin != "" {
				env.Stdin = strings.NewReader(tc.stdin)
			}

			require.NoError(t, tail(context.Background(), env, tc.args))

			if tc.wantNewlines > 0 {
				assert.Equal(t, tc.wantNewlines, strings.Count(stdout.String(), "\n"), "line count")
			} else {
				assert.Equal(t, tc.wantStdout, stdout.String(), "stdout")
			}
		})
	}
}
