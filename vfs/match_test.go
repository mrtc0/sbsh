package vfs_test

import (
	"testing"

	"github.com/mrtc0/sbsh/vfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePattern_Rejects(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		pattern string
	}{
		"empty":                  {pattern: ""},
		"question mark":          {pattern: "*.?nv"},
		"character class":        {pattern: "[abc].env"},
		"backslash":              {pattern: `a\b`},
		"doublestar with prefix": {pattern: "foo**/bar"},
		"doublestar with suffix": {pattern: "**foo/bar"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := vfs.ParsePattern(tc.pattern)
			assert.Error(t, err)
		})
	}
}

func TestPattern_Match(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		pattern string
		matches []string
		misses  []string
	}{
		"relative pattern is anchored at any depth": {
			pattern: ".env",
			matches: []string{"/.env", "/work/.env", "/work/a/b/.env"},
			misses:  []string{"/env", "/work/.envrc", "/work/.env/inner"},
		},
		"explicit doublestar prefix behaves the same": {
			pattern: "**/.env",
			matches: []string{"/.env", "/work/.env"},
			misses:  []string{"/work/prod.env"},
		},
		"star stays within one segment": {
			pattern: "/work/*",
			matches: []string{"/work/a", "/work/.env"},
			misses:  []string{"/work", "/work/a/b"},
		},
		"star combines with literals": {
			pattern: "*.env",
			matches: []string{"/prod.env", "/work/prod.env"},
			misses:  []string{"/work/env", "/work/prod.env/inner"},
		},
		"trailing doublestar covers the directory and everything under it": {
			pattern: "/work/secrets/**",
			matches: []string{"/work/secrets", "/work/secrets/db", "/work/secrets/db/pass.txt"},
			misses:  []string{"/work", "/work/other", "/worksecrets"},
		},
		"doublestar in the middle spans zero or more segments": {
			pattern: "/work/**/tmp",
			matches: []string{"/work/tmp", "/work/a/tmp", "/work/a/b/tmp"},
			misses:  []string{"/tmp", "/work/tmp/inner"},
		},
		"root matches only itself": {
			pattern: "/",
			matches: []string{"/"},
			misses:  []string{"/work"},
		},
		"names are normalized before matching": {
			pattern: "**/.env",
			matches: []string{"work/.env", "/work/./.env", "/work/a/../.env"},
			misses:  []string{"/work/a/.env/.."},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			p, err := vfs.ParsePattern(tc.pattern)
			require.NoError(t, err)

			for _, m := range tc.matches {
				assert.True(t, p.Match(m), "pattern %q should match %q", tc.pattern, m)
			}
			for _, m := range tc.misses {
				assert.False(t, p.Match(m), "pattern %q should not match %q", tc.pattern, m)
			}
		})
	}
}
