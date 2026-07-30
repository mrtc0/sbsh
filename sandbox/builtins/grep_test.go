package builtins

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_grep(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		seed         map[string]string
		stdin        string
		args         []string
		wantStdout   string
		wantContains []string
		wantExit1    bool
		wantErr      bool
	}{
		"prints matching lines": {
			seed:       map[string]string{"/work/f": "foo\nbar\nfoobar\n"},
			args:       []string{"foo", "f"},
			wantStdout: "foo\nfoobar\n",
		},
		"-E matches an extended regular expression": {
			seed:       map[string]string{"/work/f": "foo\nbar\nbaz\n"},
			args:       []string{"-E", "foo|baz", "f"},
			wantStdout: "foo\nbaz\n",
		},
		"-E supports unescaped repetition operators": {
			seed:       map[string]string{"/work/f": "color\ncolour\ncolr\n"},
			args:       []string{"-E", "colou?r", "f"},
			wantStdout: "color\ncolour\n",
		},
		"-iE combines case-insensitive and extended": {
			seed:       map[string]string{"/work/f": "Foo\nBAZ\nbar\n"},
			args:       []string{"-iE", "foo|baz", "f"},
			wantStdout: "Foo\nBAZ\n",
		},
		"-i matches case-insensitively": {
			seed:       map[string]string{"/work/f": "Hello\nworld\n"},
			args:       []string{"-i", "hello", "f"},
			wantStdout: "Hello\n",
		},
		"-v inverts the match": {
			seed:       map[string]string{"/work/f": "keep\ndrop\nkeep2\n"},
			args:       []string{"-v", "drop", "f"},
			wantStdout: "keep\nkeep2\n",
		},
		"-n prefixes line numbers": {
			seed:       map[string]string{"/work/f": "x\nmatch\n"},
			args:       []string{"-n", "match", "f"},
			wantStdout: "2:match\n",
		},
		"reads stdin when no file is given": {
			stdin:      "one\ntwo\n",
			args:       []string{"two"},
			wantStdout: "two\n",
		},
		"-r searches a directory and prefixes file names": {
			seed:       map[string]string{"/work/d/a": "hit\n"},
			args:       []string{"-r", "hit", "d"},
			wantStdout: "d/a:hit\n",
		},
		"-r names match the non-recursive form for a relative root": {
			seed:       map[string]string{"/work/dir/a": "hit\n", "/work/dir/sub/b": "hit\n"},
			args:       []string{"-r", "hit", "dir"},
			wantStdout: "dir/a:hit\ndir/sub/b:hit\n",
		},
		"-r trailing slash on root joins with a single separator": {
			seed:       map[string]string{"/work/dir/a": "hit\n", "/work/dir/sub/b": "hit\n"},
			args:       []string{"-r", "hit", "dir/"},
			wantStdout: "dir/a:hit\ndir/sub/b:hit\n",
		},
		"-r on the current directory keeps the ./ prefix": {
			seed:       map[string]string{"/work/dir/a": "hit\n"},
			args:       []string{"-r", "hit", "."},
			wantStdout: "./dir/a:hit\n",
		},
		"-r on an absolute root stays absolute": {
			seed:       map[string]string{"/work/dir/a": "hit\n"},
			args:       []string{"-r", "hit", "/work/dir"},
			wantStdout: "/work/dir/a:hit\n",
		},
		"-c prints the match count": {
			seed:       map[string]string{"/work/f": "foo\nbar\nfoo\n"},
			args:       []string{"-c", "foo", "f"},
			wantStdout: "2\n",
		},
		"-c with multiple files prefixes the name": {
			seed:         map[string]string{"/work/a": "foo\n", "/work/b": "foo\nfoo\n"},
			args:         []string{"-c", "foo", "a", "b"},
			wantContains: []string{"a:1", "b:2"},
		},
		"-l lists only names of files that match": {
			seed:       map[string]string{"/work/f": "foo\nfoo\n"},
			args:       []string{"-l", "foo", "f"},
			wantStdout: "f\n",
		},
		"-c reports zero and exits 1 when nothing matches": {
			seed:       map[string]string{"/work/f": "bar\n"},
			args:       []string{"-c", "foo", "f"},
			wantStdout: "0\n",
			wantExit1:  true,
		},
		"returns exit status 1 when nothing matches": {
			seed:      map[string]string{"/work/f": "nothing here\n"},
			args:      []string{"zzz", "f"},
			wantExit1: true,
		},
		"returns an error for an invalid regexp": {
			stdin:   "",
			args:    []string{"("},
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

			err := grep(context.Background(), env, tc.args)

			switch {
			case tc.wantErr:
				require.Error(t, err)
			case tc.wantExit1:
				var ee exitError
				require.ErrorAs(t, err, &ee)
				assert.Equal(t, 1, ee.code)
			default:
				require.NoError(t, err)
				if tc.wantContains != nil {
					for _, want := range tc.wantContains {
						assert.Contains(t, stdout.String(), want)
					}
				} else {
					assert.Equal(t, tc.wantStdout, stdout.String())
				}
			}
		})
	}
}

// A single denied file must not disable the search. GNU grep warns, covers the
// rest of the tree, and reports the error through exit status 2 — which stays
// distinguishable from the 1 that means "nothing matched".
func Test_grep_recursiveContinuesPastDeniedEntries(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		patterns     []string
		seed         map[string]string
		args         []string
		wantContains []string
		denyStdout   []string
	}{
		"a denied file does not hide its siblings": {
			patterns:     []string{"**/.env"},
			seed:         map[string]string{"/work/.env": "hello=SECRET\n", "/work/a.txt": "hello a\n", "/work/b.txt": "hello b\n"},
			args:         []string{"-r", "hello", "/work"},
			wantContains: []string{"a.txt:hello a", "b.txt:hello b"},
			denyStdout:   []string{"SECRET"},
		},
		"a denied directory does not stop the walk": {
			patterns:     []string{"/work/secrets"},
			seed:         map[string]string{"/work/secrets/db": "hello=SECRET\n", "/work/a.txt": "hello a\n"},
			args:         []string{"-r", "hello", "/work"},
			wantContains: []string{"a.txt:hello a"},
			denyStdout:   []string{"SECRET"},
		},
		"a refusal outranks having found no match": {
			patterns: []string{"**/.env"},
			seed:     map[string]string{"/work/.env": "zzz\n", "/work/a.txt": "nothing here\n"},
			args:     []string{"-r", "zzz", "/work"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			env, base, stdout, stderr := NewTestEnvWithDeny(t, "/work", "grep", tc.patterns...)
			for path, body := range tc.seed {
				mustWrite(t, base, path, body)
			}

			err := grep(context.Background(), env, tc.args)

			var ee exitError
			require.ErrorAs(t, err, &ee)
			assert.Equal(t, 2, ee.code, "a refused entry exits 2, not 1")

			for _, want := range tc.wantContains {
				assert.Contains(t, stdout.String(), want)
			}
			for _, unwanted := range tc.denyStdout {
				assert.NotContains(t, stdout.String(), unwanted)
			}
			assert.Contains(t, stderr.String(), "permission denied")
			assert.Contains(t, stderr.String(), "grep:")
		})
	}
}
