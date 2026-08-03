package builtins

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_jq(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		seed  map[string]string
		stdin string
		args  []string
		want  string
	}{
		"identity pretty-prints": {
			stdin: `{"a":1}`,
			args:  []string{"."},
			want:  "{\n  \"a\": 1\n}\n",
		},
		"-c compact output": {
			stdin: `{"a":1,"b":2}`,
			args:  []string{"-c", "."},
			want:  "{\"a\":1,\"b\":2}\n",
		},
		"field access": {
			stdin: `{"name":"sbsh"}`,
			args:  []string{"-r", ".name"},
			want:  "sbsh\n",
		},
		"-r without string still JSON-encodes": {
			stdin: `{"n":42}`,
			args:  []string{"-r", ".n"},
			want:  "42\n",
		},
		"array iteration yields one output per element": {
			stdin: `[1,2,3]`,
			args:  []string{"-c", ".[]"},
			want:  "1\n2\n3\n",
		},
		"JSON stream is processed document by document": {
			stdin: "{\"x\":1}\n{\"x\":2}\n",
			args:  []string{"-c", ".x"},
			want:  "1\n2\n",
		},
		"-s slurps the stream into an array": {
			stdin: "1 2 3",
			args:  []string{"-c", "-s", "."},
			want:  "[1,2,3]\n",
		},
		"-n null input with a constructed value": {
			args: []string{"-c", "-n", "{ok:true}"},
			want: "{\"ok\":true}\n",
		},
		"HTML characters are not escaped": {
			stdin: `{"html":"<a> & </a>"}`,
			args:  []string{"-r", ".html"},
			want:  "<a> & </a>\n",
		},
		"reads from a file": {
			seed: map[string]string{"/work/data.json": `{"v":7}`},
			args: []string{"-c", ".", "data.json"},
			want: "{\"v\":7}\n",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			env, stdout, _ := NewTestEnv(t, "/work")
			for path, body := range tc.seed {
				mustWrite(t, env.FS, path, body)
			}
			if tc.seed == nil {
				env.Stdin = strings.NewReader(tc.stdin)
			}

			require.NoError(t, jqCommand(context.Background(), env, tc.args))
			assert.Equal(t, tc.want, stdout.String())
		})
	}
}

func Test_jq_exitStatus(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		stdin string
		args  []string
		code  uint8
		ok    bool
	}{
		"truthy last output exits 0": {
			stdin: `{"a":1}`,
			args:  []string{"-e", ".a"},
			ok:    true,
		},
		"false last output exits 1": {
			stdin: `{"a":false}`,
			args:  []string{"-e", ".a"},
			code:  1,
		},
		"null last output exits 1": {
			stdin: `{"a":null}`,
			args:  []string{"-e", ".a"},
			code:  1,
		},
		"no output exits 4": {
			stdin: `{"a":1}`,
			args:  []string{"-e", ".b // empty"},
			code:  4,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			env, _, _ := NewTestEnv(t, "/work")
			env.Stdin = strings.NewReader(tc.stdin)

			err := jqCommand(context.Background(), env, tc.args)
			if tc.ok {
				require.NoError(t, err)
				return
			}
			var ee exitError
			require.ErrorAs(t, err, &ee)
			assert.Equal(t, tc.code, uint8(ee.code))
		})
	}
}

func Test_jq_errors(t *testing.T) {
	t.Parallel()

	t.Run("no filter", func(t *testing.T) {
		t.Parallel()
		env, _, _ := NewTestEnv(t, "/work")
		require.Error(t, jqCommand(context.Background(), env, nil))
	})

	t.Run("invalid filter", func(t *testing.T) {
		t.Parallel()
		env, _, _ := NewTestEnv(t, "/work")
		env.Stdin = strings.NewReader(`{}`)
		require.Error(t, jqCommand(context.Background(), env, []string{".["}))
	})
}
