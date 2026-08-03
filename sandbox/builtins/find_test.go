package builtins

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrtc0/sbsh/sandbox/command"
)

func lines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	out := strings.Split(s, "\n")
	sort.Strings(out)
	return out
}

func Test_find(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		seed    map[string]string
		mkdirs  []string
		args    []string
		want    []string
		wantErr bool
	}{
		"lists everything under the root": {
			seed: map[string]string{"/work/a": "", "/work/sub/b": ""},
			args: []string{"."},
			want: []string{".", "./a", "./sub", "./sub/b"},
		},
		"defaults to the current directory": {
			seed: map[string]string{"/work/only": ""},
			args: nil,
			want: []string{".", "./only"},
		},
		"relative root keeps its typed form": {
			seed: map[string]string{"/work/dir/a": "", "/work/dir/sub/b": ""},
			args: []string{"dir"},
			want: []string{"dir", "dir/a", "dir/sub", "dir/sub/b"},
		},
		"trailing slash on root joins with a single separator": {
			seed: map[string]string{"/work/dir/a": "", "/work/dir/sub/b": ""},
			args: []string{"dir/"},
			want: []string{"dir/", "dir/a", "dir/sub", "dir/sub/b"},
		},
		"absolute root stays absolute": {
			seed: map[string]string{"/work/dir/a": ""},
			args: []string{"/work/dir"},
			want: []string{"/work/dir", "/work/dir/a"},
		},
		"-type f keeps only files": {
			seed:   map[string]string{"/work/a": ""},
			mkdirs: []string{"/work/d"},
			args:   []string{".", "-type", "f"},
			want:   []string{"./a"},
		},
		"-type d keeps only directories": {
			seed:   map[string]string{"/work/a": ""},
			mkdirs: []string{"/work/d"},
			args:   []string{".", "-type", "d"},
			want:   []string{".", "./d"},
		},
		"-name matches base names by glob": {
			seed: map[string]string{"/work/a.txt": "", "/work/b.go": "", "/work/sub/c.txt": ""},
			args: []string{".", "-name", "*.txt"},
			want: []string{"./a.txt", "./sub/c.txt"},
		},
		"errors on an unknown option": {
			args:    []string{"-bogus"},
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
			for _, dir := range tc.mkdirs {
				mustMkdir(t, env.FS, dir)
			}

			env.Args = tc.args
			err := find(context.Background(), env)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, lines(stdout.String()))
		})
	}
}

// One denied entry must not cut the listing short. GNU find reports what it
// cannot read, walks the rest, and exits 1.
func Test_find_continuesPastDeniedEntries(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		patterns []string
		seed     map[string]string
		args     []string
		want     []string
	}{
		"a denied file does not hide its siblings": {
			patterns: []string{"**/.env"},
			seed:     map[string]string{"/work/.env": "", "/work/a.txt": "", "/work/sub/b.txt": ""},
			args:     []string{"/work"},
			want:     []string{"/work", "/work/a.txt", "/work/sub", "/work/sub/b.txt"},
		},
		"a denied directory does not stop the walk": {
			patterns: []string{"/work/secrets"},
			seed:     map[string]string{"/work/secrets/db": "", "/work/a.txt": ""},
			args:     []string{"/work"},
			want:     []string{"/work", "/work/a.txt"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			env, base, stdout, stderr := NewTestEnvWithDeny(t, "/work", "find", tc.patterns...)
			for p, body := range tc.seed {
				mustWrite(t, base, p, body)
			}

			env.Args = tc.args
			err := find(context.Background(), env)

			var ee *command.ExitError
			require.ErrorAs(t, err, &ee)
			assert.Equal(t, 1, ee.Code)
			assert.Equal(t, tc.want, lines(stdout.String()))
			assert.Contains(t, stderr.String(), "find:")
			assert.Contains(t, stderr.String(), "permission denied")
		})
	}
}
