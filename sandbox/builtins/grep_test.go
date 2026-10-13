package builtins

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrtc0/sbsh/sandbox/command"
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
		"-A prints trailing context": {
			seed:       map[string]string{"/work/f": "1\n2\nhit\n4\n5\n"},
			args:       []string{"-A", "1", "hit", "f"},
			wantStdout: "hit\n4\n",
		},
		"-B prints leading context": {
			seed:       map[string]string{"/work/f": "1\n2\nhit\n4\n5\n"},
			args:       []string{"-B", "1", "hit", "f"},
			wantStdout: "2\nhit\n",
		},
		"-A and -B together bracket the match": {
			seed:       map[string]string{"/work/f": "1\n2\nhit\n4\n5\n"},
			args:       []string{"-A", "1", "-B", "1", "hit", "f"},
			wantStdout: "2\nhit\n4\n",
		},
		"context is capped at the start and end of the input": {
			seed:       map[string]string{"/work/f": "a\nhit\nb\n"},
			args:       []string{"-A", "9", "-B", "9", "hit", "f"},
			wantStdout: "a\nhit\nb\n",
		},
		"a count far past the end does not overflow": {
			seed:       map[string]string{"/work/f": "a\nhit\nb\n"},
			args:       []string{"-A", "9223372036854775807", "-B", "9223372036854775807", "hit", "f"},
			wantStdout: "a\nhit\nb\n",
		},
		"-A0 keeps the selected lines only": {
			seed:       map[string]string{"/work/f": "hit\n2\nhit\n"},
			args:       []string{"-A", "0", "hit", "f"},
			wantStdout: "hit\n--\nhit\n",
		},
		"-B0 keeps the selected lines only": {
			seed:       map[string]string{"/work/f": "hit\n2\nhit\n"},
			args:       []string{"-B", "0", "hit", "f"},
			wantStdout: "hit\n--\nhit\n",
		},
		"overlapping ranges are merged without repeating a line": {
			seed:       map[string]string{"/work/f": "1\nhit\n3\nhit\n5\n"},
			args:       []string{"-A", "2", "-B", "2", "hit", "f"},
			wantStdout: "1\nhit\n3\nhit\n5\n",
		},
		"adjacent ranges are joined without a separator": {
			seed:       map[string]string{"/work/f": "1\nhit\n3\n4\nhit\n6\n"},
			args:       []string{"-A", "1", "-B", "1", "hit", "f"},
			wantStdout: "1\nhit\n3\n4\nhit\n6\n",
		},
		"separated ranges are parted by --": {
			seed:       map[string]string{"/work/f": "1\nhit\n3\n4\n5\nhit\n7\n"},
			args:       []string{"-A", "1", "-B", "1", "hit", "f"},
			wantStdout: "1\nhit\n3\n--\n5\nhit\n7\n",
		},
		"a later match inside the lines already printed adds nothing": {
			seed:       map[string]string{"/work/f": "hit\nhit\n3\n4\n"},
			args:       []string{"-A", "2", "hit", "f"},
			wantStdout: "hit\nhit\n3\n4\n",
		},
		"no separator before the first group or after the last": {
			seed:       map[string]string{"/work/f": "hit\n"},
			args:       []string{"-A", "1", "-B", "1", "hit", "f"},
			wantStdout: "hit\n",
		},
		"context lines take a - prefix separator": {
			seed:       map[string]string{"/work/f": "1\nhit\n3\n"},
			args:       []string{"-n", "-A", "1", "-B", "1", "hit", "f"},
			wantStdout: "1-1\n2:hit\n3-3\n",
		},
		"-A combines in a short group": {
			seed:       map[string]string{"/work/f": "1\nhit\n3\n"},
			args:       []string{"-nA2", "hit", "f"},
			wantStdout: "2:hit\n3-3\n",
		},
		"-A accepts an attached value": {
			seed:       map[string]string{"/work/f": "1\nhit\n3\n"},
			args:       []string{"-A1", "hit", "f"},
			wantStdout: "hit\n3\n",
		},
		"the last context value wins": {
			seed:       map[string]string{"/work/f": "1\nhit\n3\n4\n"},
			args:       []string{"-A", "2", "-A", "1", "hit", "f"},
			wantStdout: "hit\n3\n",
		},
		"-v selects the lines context is measured from": {
			seed:       map[string]string{"/work/f": "1\n2\ndrop\n4\n"},
			args:       []string{"-v", "-A", "1", "[0-9]", "f"},
			wantStdout: "drop\n4\n",
		},
		"-i and -E choose the same selected lines context follows": {
			seed:       map[string]string{"/work/f": "1\nHIT\n3\n"},
			args:       []string{"-iE", "-A", "1", "h(i)t", "f"},
			wantStdout: "HIT\n3\n",
		},
		"context lines carry the file name when it is shown": {
			seed:         map[string]string{"/work/a": "1\nhit\n3\n", "/work/b": "x\n"},
			args:         []string{"-A", "1", "hit", "a", "b"},
			wantContains: []string{"a:hit\na-3\n"},
		},
		"groups in different files are parted by --": {
			seed:       map[string]string{"/work/a": "hit\n2\n", "/work/b": "hit\n2\n"},
			args:       []string{"-A", "1", "hit", "a", "b"},
			wantStdout: "a:hit\na-2\n--\nb:hit\nb-2\n",
		},
		"a file with no match adds no separator": {
			seed:       map[string]string{"/work/a": "hit\n", "/work/b": "x\n", "/work/c": "hit\n"},
			args:       []string{"-A", "1", "hit", "a", "b", "c"},
			wantStdout: "a:hit\n--\nc:hit\n",
		},
		"context ranges do not reach across a file boundary": {
			seed:       map[string]string{"/work/a": "1\n2\nhit\n", "/work/b": "1\n2\n3\n"},
			args:       []string{"-A", "5", "hit", "a", "b"},
			wantStdout: "a:hit\n",
		},
		"-r reports context under the walked name": {
			seed:       map[string]string{"/work/d/a": "1\nhit\n3\n"},
			args:       []string{"-r", "-A", "1", "hit", "d"},
			wantStdout: "d/a:hit\nd/a-3\n",
		},
		"context reads from stdin too": {
			stdin:      "1\nhit\n3\n",
			args:       []string{"-A", "1", "hit"},
			wantStdout: "hit\n3\n",
		},
		"-c ignores context and counts selected lines only": {
			seed:       map[string]string{"/work/f": "1\nhit\n3\nhit\n"},
			args:       []string{"-c", "-A", "1", "-B", "1", "hit", "f"},
			wantStdout: "2\n",
		},
		"-l ignores context and names the source once": {
			seed:       map[string]string{"/work/f": "1\nhit\n3\nhit\n"},
			args:       []string{"-l", "-A", "1", "hit", "f"},
			wantStdout: "f\n",
		},
		"-cl keeps -l winning over -c with context asked for": {
			seed:       map[string]string{"/work/f": "1\nhit\n3\n"},
			args:       []string{"-c", "-l", "-A", "1", "hit", "f"},
			wantStdout: "f\n",
		},
		"-c still rejects a bad context length": {
			seed:    map[string]string{"/work/f": "hit\n"},
			args:    []string{"-c", "-A", "x", "hit", "f"},
			wantErr: true,
		},
		"-l still rejects a bad context length": {
			seed:    map[string]string{"/work/f": "hit\n"},
			args:    []string{"-l", "-A", "x", "hit", "f"},
			wantErr: true,
		},
		"a non-numeric context length is an error": {
			seed:    map[string]string{"/work/f": "hit\n"},
			args:    []string{"-A", "x", "hit", "f"},
			wantErr: true,
		},
		"a negative context length is an error": {
			seed:    map[string]string{"/work/f": "hit\n"},
			args:    []string{"-B", "-1", "hit", "f"},
			wantErr: true,
		},
		"a fractional context length is an error": {
			seed:    map[string]string{"/work/f": "hit\n"},
			args:    []string{"-A", "1.5", "hit", "f"},
			wantErr: true,
		},
		"an empty context length is an error": {
			seed:    map[string]string{"/work/f": "hit\n"},
			args:    []string{"-A", "", "hit", "f"},
			wantErr: true,
		},
		"a context length past the int range is an error": {
			seed:    map[string]string{"/work/f": "hit\n"},
			args:    []string{"-A", "99999999999999999999", "hit", "f"},
			wantErr: true,
		},
		"a missing context length is an error": {
			seed:    map[string]string{"/work/f": "hit\n"},
			args:    []string{"hit", "f", "-A"},
			wantErr: true,
		},
		"an earlier bad context length is still an error": {
			seed:    map[string]string{"/work/f": "hit\n"},
			args:    []string{"-A", "x", "-A", "1", "hit", "f"},
			wantErr: true,
		},
		"context on an empty input selects nothing": {
			seed:      map[string]string{"/work/f": ""},
			args:      []string{"-A", "2", "hit", "f"},
			wantExit1: true,
		},
		"context on an input with no match prints no separator": {
			seed:      map[string]string{"/work/f": "a\nb\n"},
			args:      []string{"-A", "2", "hit", "f"},
			wantExit1: true,
		},
		"a blank context line is printed as a blank line": {
			seed:       map[string]string{"/work/f": "hit\n\n"},
			args:       []string{"-A", "1", "hit", "f"},
			wantStdout: "hit\n\n",
		},
		"a final line without a newline still gains one": {
			seed:       map[string]string{"/work/f": "hit\n3"},
			args:       []string{"-A", "1", "hit", "f"},
			wantStdout: "hit\n3\n",
		},
		"a CR is kept on a context line": {
			seed:       map[string]string{"/work/f": "a\r\nhit\r\nb\r\n"},
			args:       []string{"-A", "1", "hit", "f"},
			wantStdout: "hit\r\nb\r\n",
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

			inv, stdout, stderr := NewTestEnv(t, "/work")
			for path, body := range tc.seed {
				mustWrite(t, inv.FS, path, body)
			}
			if tc.seed == nil {
				inv.Stdin = strings.NewReader(tc.stdin)
			}

			inv.Args = tc.args
			err := grep(context.Background(), inv)

			switch {
			case tc.wantErr:
				// A failure the caller should see carries its own message, so the
				// sandbox has something to print on grep's behalf.
				var ee *command.ExitError
				require.ErrorAs(t, err, &ee)
				assert.Equal(t, 1, ee.Code)
				assert.NotEmpty(t, ee.Msg, "a failure carries the message to display")
			case tc.wantExit1:
				// "Nothing matched" is a status, not a diagnostic: no message, and
				// nothing written on the way out either.
				var ee *command.ExitError
				require.ErrorAs(t, err, &ee)
				assert.Equal(t, 1, ee.Code)
				assert.Empty(t, ee.Msg, "no match is a status, not a message")
				assert.Empty(t, stderr.String(), "stderr")
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

			inv, base, stdout, stderr := NewTestEnvWithDeny(t, "/work", "grep", tc.patterns...)
			for path, body := range tc.seed {
				mustWrite(t, base, path, body)
			}

			inv.Args = tc.args
			err := grep(context.Background(), inv)

			var ee *command.ExitError
			require.ErrorAs(t, err, &ee)
			assert.Equal(t, 2, ee.Code, "a refused entry exits 2, not 1")

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
