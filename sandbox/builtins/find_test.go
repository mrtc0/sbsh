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
		"-path matches the whole printed path": {
			seed: map[string]string{"/work/a.txt": "", "/work/sub/b.txt": "", "/work/sub/c.go": ""},
			args: []string{".", "-path", "*.txt"},
			want: []string{"./a.txt", "./sub/b.txt"},
		},
		"-path matches the root itself": {
			seed: map[string]string{"/work/dir/a": ""},
			args: []string{"dir", "-path", "dir"},
			want: []string{"dir"},
		},
		"-path sees the typed trailing slash": {
			seed: map[string]string{"/work/dir/a": ""},
			args: []string{"dir/", "-path", "dir/"},
			want: []string{"dir/"},
		},
		"-path sees the absolute root as typed": {
			seed: map[string]string{"/work/dir/a": "", "/work/dir/sub/b": ""},
			args: []string{"/work/dir", "-path", "/work/dir/*/*"},
			want: []string{"/work/dir/sub/b"},
		},
		"-path applies to every root": {
			seed: map[string]string{"/work/x/a.txt": "", "/work/y/a.txt": "", "/work/y/b.go": ""},
			args: []string{"x", "y", "-path", "*/a.txt"},
			want: []string{"x/a.txt", "y/a.txt"},
		},
		"-path wildcards cross directory boundaries": {
			seed: map[string]string{"/work/sub/deep/a.txt": "", "/work/b.txt": ""},
			args: []string{".", "-path", "./*a.txt"},
			want: []string{"./sub/deep/a.txt"},
		},
		"-path matches leading dots": {
			seed: map[string]string{"/work/.hidden/a": ""},
			args: []string{".", "-path", "./*/a"},
			want: []string{"./.hidden/a"},
		},
		"-path must match entirely": {
			seed: map[string]string{"/work/sub/a.txt": ""},
			args: []string{".", "-path", "sub"},
			want: nil,
		},
		"-path keeps walking past directories that do not match": {
			seed: map[string]string{"/work/sub/deep/a.txt": ""},
			args: []string{".", "-path", "*/deep/a.txt"},
			want: []string{"./sub/deep/a.txt"},
		},
		"-path honours escapes and character classes": {
			seed: map[string]string{"/work/a*b": "", "/work/axb": "", "/work/a1b": ""},
			args: []string{".", "-path", `./a\*b`},
			want: []string{"./a*b"},
		},
		"-path character class matches one of a set": {
			seed: map[string]string{"/work/a1b": "", "/work/axb": ""},
			args: []string{".", "-path", "./a[0-9]b"},
			want: []string{"./a1b"},
		},
		"-path matches Unicode names": {
			seed: map[string]string{"/work/\u65e5\u672c\u8a9e/\u30d5\u30a1\u30a4\u30eb.txt": ""},
			args: []string{".", "-path", "./\u65e5\u672c\u8a9e/*.txt"},
			want: []string{"./\u65e5\u672c\u8a9e/\u30d5\u30a1\u30a4\u30eb.txt"},
		},
		"-path ? matches a single rune": {
			seed: map[string]string{"/work/\u3042.txt": ""},
			args: []string{".", "-path", "./?.txt"},
			want: []string{"./\u3042.txt"},
		},
		"-path with an empty pattern matches nothing": {
			seed: map[string]string{"/work/a": ""},
			args: []string{".", "-path", ""},
			want: nil,
		},
		"-path takes the last of repeated options": {
			seed: map[string]string{"/work/a.txt": "", "/work/b.go": ""},
			args: []string{".", "-path", "*.txt", "-path", "*.go"},
			want: []string{"./b.go"},
		},
		"-path ANDs with -name and -type": {
			seed:   map[string]string{"/work/sub/x.txt": "", "/work/sub/y.txt": "", "/work/x.txt": ""},
			mkdirs: []string{"/work/sub/xdir"},
			args:   []string{".", "-path", "./sub/*", "-name", "x*", "-type", "f"},
			want:   []string{"./sub/x.txt"},
		},
		"errors when -path has no argument": {
			args:    []string{".", "-path"},
			wantErr: true,
		},
		"errors on an invalid -path pattern": {
			seed:    map[string]string{"/work/a": ""},
			args:    []string{".", "-path", "[a-"},
			wantErr: true,
		},
		"errors on an unknown option": {
			args:    []string{"-bogus"},
			wantErr: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			inv, stdout, _ := NewTestEnv(t, "/work")
			for path, body := range tc.seed {
				mustWrite(t, inv.FS, path, body)
			}
			for _, dir := range tc.mkdirs {
				mustMkdir(t, inv.FS, dir)
			}

			inv.Args = tc.args
			err := find(context.Background(), inv)
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
		"-path still reports a denied entry and keeps its siblings": {
			patterns: []string{"**/.env"},
			seed:     map[string]string{"/work/.env": "", "/work/a.txt": "", "/work/sub/b.txt": ""},
			args:     []string{"/work", "-path", "*.txt"},
			want:     []string{"/work/a.txt", "/work/sub/b.txt"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			inv, base, stdout, stderr := NewTestEnvWithDeny(t, "/work", "find", tc.patterns...)
			for p, body := range tc.seed {
				mustWrite(t, base, p, body)
			}

			inv.Args = tc.args
			err := find(context.Background(), inv)

			var ee *command.ExitError
			require.ErrorAs(t, err, &ee)
			assert.Equal(t, 1, ee.Code)
			assert.Equal(t, tc.want, lines(stdout.String()))
			assert.Contains(t, stderr.String(), "find:")
			assert.Contains(t, stderr.String(), "permission denied")
		})
	}
}

// -path is matched against the printed name as a whole, so the matcher is
// tested directly for names a line-oriented assertion cannot express.
func Test_find_compilePathPattern(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		pattern string
		name    string
		want    bool
	}{
		"* matches a newline in a name":       {pattern: "./*", name: "./a\nb", want: true},
		"? matches a newline in a name":       {pattern: "./a?b", name: "./a\nb", want: true},
		"a literal newline must match":        {pattern: "./a\nb", name: "./a\nb", want: true},
		"** is just two stars":                {pattern: "./**/a", name: "./x/y/a", want: true},
		"extglob is matched literally":        {pattern: "./!(x)", name: "./!(x)", want: true},
		"extglob does not negate":             {pattern: "./!(x)", name: "./y", want: false},
		"the match must cover the whole name": {pattern: "sub", name: "./sub/a", want: false},
		"an empty pattern matches nothing":    {pattern: "", name: ".", want: false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			re, err := compilePathPattern(tc.pattern)
			require.NoError(t, err)
			assert.Equal(t, tc.want, re.MatchString(tc.name))
		})
	}
}

func Test_find_compilePathPattern_rejectsBadPatterns(t *testing.T) {
	t.Parallel()

	for _, pat := range []string{"[", "[a-"} {
		t.Run(pat, func(t *testing.T) {
			t.Parallel()

			_, err := compilePathPattern(pat)
			assert.Error(t, err)
		})
	}
}
