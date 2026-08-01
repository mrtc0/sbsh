package builtins

import (
	"errors"
	"os"
	"path"
	"syscall"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_walkGuard(t *testing.T) {
	t.Parallel()

	// body decides what the walk's own callback reports for a given entry, so a
	// case can exercise a refusal raised by the traversal or by the body.
	cases := map[string]struct {
		body        func(p string) error
		wantVisited []string
		wantRefused bool
		wantErrStr  string
		wantWarning string
	}{
		"a refusal from the traversal is skipped": {
			wantVisited: []string{"/work/a.txt", "/work/b.txt"},
			wantRefused: true,
			wantWarning: "lstat /work/.env: permission denied",
		},
		"a refusal from the body is skipped": {
			body: func(p string) error {
				if path.Base(p) == "b.txt" {
					return &os.PathError{Op: "open", Path: p, Err: syscall.EACCES}
				}
				return nil
			},
			wantVisited: []string{"/work/a.txt", "/work/b.txt"},
			wantRefused: true,
			wantWarning: "open /work/b.txt: permission denied",
		},
		// ".env" is visited before "a.txt", so this case also pins that a refusal
		// already reported stays recorded when a later error ends the walk.
		"any other error stops the walk": {
			body: func(p string) error {
				if path.Base(p) == "a.txt" {
					return errors.New("broken")
				}
				return nil
			},
			wantVisited: []string{"/work/a.txt"},
			wantRefused: true,
			wantWarning: "lstat /work/.env: permission denied",
			wantErrStr:  "broken",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			env, base, _, stderr := NewTestEnvWithDeny(t, "/work", "walktest", "**/.env")
			for _, p := range []string{"/work/.env", "/work/a.txt", "/work/b.txt"} {
				mustWrite(t, base, p, "x")
			}

			var visited []string
			guard := &walkGuard{inv: env}
			err := afero.Walk(env.FS, "/work", guard.wrap(func(p string, info os.FileInfo, _ error) error {
				if info.IsDir() {
					return nil
				}
				visited = append(visited, p)
				if tc.body == nil {
					return nil
				}
				return tc.body(p)
			}))

			if tc.wantErrStr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErrStr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.wantVisited, visited)
			assert.Equal(t, tc.wantRefused, guard.refused)
			if tc.wantWarning != "" {
				assert.Contains(t, stderr.String(), "walktest: "+tc.wantWarning)
			} else {
				assert.Empty(t, stderr.String())
			}
		})
	}
}
