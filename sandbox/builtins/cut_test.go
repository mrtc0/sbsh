package builtins

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_cut(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		seed    map[string]string
		stdin   string
		args    []string
		want    string
		wantErr bool
	}{
		"fields with default tab delimiter": {
			stdin: "a\tb\tc\n",
			args:  []string{"-f", "2"},
			want:  "b\n",
		},
		"fields with custom delimiter": {
			stdin: "a,b,c\n",
			args:  []string{"-d", ",", "-f", "1,3"},
			want:  "a,c\n",
		},
		"field range": {
			stdin: "1:2:3:4\n",
			args:  []string{"-d", ":", "-f", "2-3"},
			want:  "2:3\n",
		},
		"open-ended range to end": {
			stdin: "1:2:3:4\n",
			args:  []string{"-d", ":", "-f", "3-"},
			want:  "3:4\n",
		},
		"open-ended range from start": {
			stdin: "1:2:3:4\n",
			args:  []string{"-d", ":", "-f", "-2"},
			want:  "1:2\n",
		},
		"characters": {
			stdin: "hello\n",
			args:  []string{"-c", "1-3"},
			want:  "hel\n",
		},
		"attached option value": {
			stdin: "a,b,c\n",
			args:  []string{"-d,", "-f2"},
			want:  "b\n",
		},
		"line without delimiter is passed through": {
			stdin: "nodelim\n",
			args:  []string{"-d", ",", "-f", "2"},
			want:  "nodelim\n",
		},
		"-s drops lines without delimiter": {
			stdin: "a,b\nnodelim\nc,d\n",
			args:  []string{"-d", ",", "-f", "1", "-s"},
			want:  "a\nc\n",
		},
		"reads a file": {
			seed: map[string]string{"/work/data": "x,y\n"},
			args: []string{"-d", ",", "-f", "2", "data"},
			want: "y\n",
		},
		"requires -f or -c": {
			stdin:   "a\n",
			args:    []string{"a"},
			wantErr: true,
		},
		"rejects both -f and -c": {
			stdin:   "a\n",
			args:    []string{"-f", "1", "-c", "1"},
			wantErr: true,
		},
		"invalid range errors": {
			stdin:   "a\n",
			args:    []string{"-f", "x"},
			wantErr: true,
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
				env.HC.Stdin = strings.NewReader(tc.stdin)
			}

			err := cut(context.Background(), env, tc.args)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, stdout.String())
		})
	}
}
