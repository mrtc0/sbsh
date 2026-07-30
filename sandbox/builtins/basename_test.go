package builtins

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_basename(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		args    []string
		want    string
		wantErr bool
	}{
		"strips directory":          {args: []string{"/usr/lib/libc.so"}, want: "libc.so\n"},
		"plain name is unchanged":   {args: []string{"file.txt"}, want: "file.txt\n"},
		"removes trailing slash":    {args: []string{"/usr/lib/"}, want: "lib\n"},
		"removes matching suffix":   {args: []string{"/a/file.txt", ".txt"}, want: "file\n"},
		"keeps non-matching suffix": {args: []string{"/a/file.txt", ".md"}, want: "file.txt\n"},
		"suffix equal to base kept": {args: []string{"/a/.txt", ".txt"}, want: ".txt\n"},
		"root reduces to slash":     {args: []string{"/"}, want: "/\n"},
		"no args errors":            {args: nil, wantErr: true},
		"too many args errors":      {args: []string{"a", "b", "c"}, wantErr: true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			env, stdout, _ := NewTestEnv(t, "/work")
			err := basename(context.Background(), env, tc.args)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, stdout.String())
		})
	}
}

func Test_dirname(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		args    []string
		want    string
		wantErr bool
	}{
		"parent directory":       {args: []string{"/usr/lib/libc.so"}, want: "/usr/lib\n"},
		"plain name is dot":      {args: []string{"file.txt"}, want: ".\n"},
		"trailing slash ignored": {args: []string{"/usr/lib/"}, want: "/usr\n"},
		"root stays root":        {args: []string{"/"}, want: "/\n"},
		"multiple paths":         {args: []string{"/a/b", "/c/d"}, want: "/a\n/c\n"},
		"no args errors":         {args: nil, wantErr: true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			env, stdout, _ := NewTestEnv(t, "/work")
			err := dirname(context.Background(), env, tc.args)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, stdout.String())
		})
	}
}
