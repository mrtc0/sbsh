package builtins

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_ls(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		seedFiles    map[string]string
		seedDirs     []string
		args         []string
		want         string
		wantContains []string
		wantErr      bool
	}{
		"lists directory entries sorted": {
			seedFiles: map[string]string{"/work/banana": "", "/work/apple": ""},
			seedDirs:  []string{"/work/cherry"},
			args:      nil,
			want:      "apple\nbanana\ncherry\n",
		},
		"defaults to current directory when no path given": {
			seedFiles: map[string]string{"/work/only": ""},
			args:      []string{},
			want:      "only\n",
		},
		"prints the path itself for a file argument": {
			seedFiles: map[string]string{"/work/file.txt": "hello"},
			args:      []string{"file.txt"},
			want:      "file.txt\n",
		},
		"resolves relative paths against Dir": {
			seedFiles: map[string]string{"/work/sub/inner": ""},
			args:      []string{"sub"},
			want:      "inner\n",
		},
		"accepts absolute paths": {
			seedFiles: map[string]string{"/other/x": ""},
			args:      []string{"/other"},
			want:      "x\n",
		},
		"-l prints a long listing with mode, size and name": {
			seedFiles:    map[string]string{"/work/data": "12345"},
			args:         []string{"-l"},
			wantContains: []string{"data", "5"},
		},
		"returns an error for a missing path": {
			args:    []string{"nope"},
			wantErr: true,
		},
		"lists multiple paths in order": {
			seedFiles: map[string]string{"/work/a/1": "", "/work/b/2": ""},
			args:      []string{"a", "b"},
			want:      "1\n2\n",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			inv, stdout, _ := NewTestEnv(t, "/work")
			for _, dir := range tc.seedDirs {
				mustMkdir(t, inv.FS, dir)
			}
			for path, content := range tc.seedFiles {
				mustWrite(t, inv.FS, path, content)
			}

			inv.Args = tc.args
			err := ls(context.Background(), inv)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			if len(tc.wantContains) > 0 {
				for _, want := range tc.wantContains {
					assert.Contains(t, stdout.String(), want)
				}
				return
			}
			assert.Equal(t, tc.want, stdout.String())
		})
	}
}

func mustWrite(t *testing.T, fs afero.Fs, path, content string) {
	t.Helper()
	if err := afero.WriteFile(fs, path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}

func mustMkdir(t *testing.T, fs afero.Fs, path string) {
	t.Helper()
	if err := fs.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", path, err)
	}
}
