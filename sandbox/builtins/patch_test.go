package builtins

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_patch(t *testing.T) {
	t.Parallel()

	t.Run("applies a modification", func(t *testing.T) {
		t.Parallel()
		env, _, _ := NewTestEnv(t, "/work")
		mustWrite(t, env.FS, "/work/f", "line1\nline2\nline3\n")
		env.Stdin = strings.NewReader(
			"--- f\n+++ f\n@@ -1,3 +1,3 @@\n line1\n-line2\n+CHANGED\n line3\n")

		env.Args = nil
		require.NoError(t, patchCommand(context.Background(), env))
		assert.Equal(t, "line1\nCHANGED\nline3\n", mustRead(t, env.FS, "/work/f"))
	})

	t.Run("fails and leaves the file untouched on context mismatch", func(t *testing.T) {
		t.Parallel()
		env, _, _ := NewTestEnv(t, "/work")
		mustWrite(t, env.FS, "/work/f", "aaa\nbbb\n")
		env.Stdin = strings.NewReader(
			"--- f\n+++ f\n@@ -1,2 +1,2 @@\n xxx\n-bbb\n+ccc\n")

		env.Args = nil
		require.Error(t, patchCommand(context.Background(), env))
		assert.Equal(t, "aaa\nbbb\n", mustRead(t, env.FS, "/work/f"))
	})

	t.Run("-p1 strips a leading path component", func(t *testing.T) {
		t.Parallel()
		env, _, _ := NewTestEnv(t, "/work")
		mustWrite(t, env.FS, "/work/f", "x\n")
		env.Stdin = strings.NewReader(
			"--- a/f\n+++ b/f\n@@ -1,1 +1,1 @@\n-x\n+y\n")

		env.Args = []string{"-p1"}
		require.NoError(t, patchCommand(context.Background(), env))
		assert.Equal(t, "y\n", mustRead(t, env.FS, "/work/f"))
	})

	t.Run("creates a file from /dev/null", func(t *testing.T) {
		t.Parallel()
		env, _, _ := NewTestEnv(t, "/work")
		env.Stdin = strings.NewReader(
			"--- /dev/null\n+++ new.txt\n@@ -0,0 +1,2 @@\n+hello\n+world\n")

		env.Args = nil
		require.NoError(t, patchCommand(context.Background(), env))
		assert.Equal(t, "hello\nworld\n", mustRead(t, env.FS, "/work/new.txt"))
	})
}

func Test_patch_confinesTargets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		files   map[string]string // seeded before the patch runs
		diff    string
		args    []string
		wantErr bool
		want    map[string]string // content every path must have afterwards
		absent  []string          // paths that must not exist afterwards
	}{
		{
			name:    "modification escaping the working directory is rejected",
			files:   map[string]string{"/escape.txt": "keep\n"},
			diff:    "--- a/../../escape.txt\n+++ b/../../escape.txt\n@@ -1,1 +1,1 @@\n-keep\n+owned\n",
			args:    []string{"-p1"},
			wantErr: true,
			want:    map[string]string{"/escape.txt": "keep\n"},
		},
		{
			name:    "creation escaping the working directory is rejected",
			diff:    "--- /dev/null\n+++ ../escape.txt\n@@ -0,0 +1,1 @@\n+owned\n",
			wantErr: true,
			absent:  []string{"/escape.txt"},
		},
		{
			name:    "deletion escaping the working directory is rejected",
			files:   map[string]string{"/escape.txt": "keep\n"},
			diff:    "--- ../escape.txt\n+++ /dev/null\n@@ -1,1 +0,0 @@\n-keep\n",
			wantErr: true,
			want:    map[string]string{"/escape.txt": "keep\n"},
		},
		{
			name:    "absolute target is rejected rather than relocated",
			diff:    "--- /dev/null\n+++ /abs.txt\n@@ -0,0 +1,1 @@\n+owned\n",
			wantErr: true,
			absent:  []string{"/abs.txt", "/work/abs.txt"},
		},
		{
			name:    "absolute deletion target is rejected",
			files:   map[string]string{"/abs.txt": "keep\n"},
			diff:    "--- /abs.txt\n+++ /dev/null\n@@ -1,1 +0,0 @@\n-keep\n",
			wantErr: true,
			want:    map[string]string{"/abs.txt": "keep\n"},
		},
		{
			name:    "no file is written when a later target escapes",
			files:   map[string]string{"/work/first.txt": "a\n"},
			diff:    "--- first.txt\n+++ first.txt\n@@ -1,1 +1,1 @@\n-a\n+b\n--- ../escape.txt\n+++ ../escape.txt\n@@ -1,1 +1,1 @@\n-keep\n+owned\n",
			wantErr: true,
			want:    map[string]string{"/work/first.txt": "a\n"},
			absent:  []string{"/escape.txt"},
		},
		{
			name:  "interior dotdot staying inside is applied",
			files: map[string]string{"/work/b.txt": "x\n"},
			diff:  "--- a/../b.txt\n+++ a/../b.txt\n@@ -1,1 +1,1 @@\n-x\n+y\n",
			want:  map[string]string{"/work/b.txt": "y\n"},
		},
		{
			name:  "absolute target is stripped down to a relative one by -pN",
			files: map[string]string{"/work/abs.txt": "x\n"},
			diff:  "--- /abs.txt\n+++ /abs.txt\n@@ -1,1 +1,1 @@\n-x\n+y\n",
			args:  []string{"-p1"},
			want:  map[string]string{"/work/abs.txt": "y\n"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env, _, _ := NewTestEnv(t, "/work")
			for p, content := range tt.files {
				mustWrite(t, env.FS, p, content)
			}
			env.Stdin = strings.NewReader(tt.diff)

			env.Args = tt.args
			err := patchCommand(context.Background(), env)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			for p, content := range tt.want {
				assert.Equal(t, content, mustRead(t, env.FS, p))
			}
			for _, p := range tt.absent {
				_, err := env.FS.Stat(p)
				assert.Errorf(t, err, "%s must not have been created", p)
			}
		})
	}
}

// Test_patch_atomicity pins what a rejected diff leaves behind. A diff naming
// several files is rejected as a whole: nothing it would have written is
// written, and nothing is reported as patched. This holds even when the reason
// for the rejection surfaces only after an earlier file's new content is
// settled — a hunk that does not apply, or a target that is missing.
func Test_patch_atomicity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		files       map[string]string
		diff        string
		args        []string
		wantErr     bool
		want        map[string]string
		absent      []string
		wantNoStdio bool // nothing may be reported as patched
	}{
		{
			name: "a later hunk mismatch leaves the earlier file untouched",
			files: map[string]string{
				"/work/a.txt": "one\n",
				"/work/b.txt": "WRONG\n",
			},
			diff: "--- a.txt\n+++ a.txt\n@@ -1,1 +1,1 @@\n-one\n+ONE\n" +
				"--- b.txt\n+++ b.txt\n@@ -1,1 +1,1 @@\n-two\n+TWO\n",
			wantErr: true,
			want: map[string]string{
				"/work/a.txt": "one\n",
				"/work/b.txt": "WRONG\n",
			},
			wantNoStdio: true,
		},
		{
			name:  "a later missing target leaves the earlier file untouched",
			files: map[string]string{"/work/a.txt": "one\n"},
			diff: "--- a.txt\n+++ a.txt\n@@ -1,1 +1,1 @@\n-one\n+ONE\n" +
				"--- gone.txt\n+++ gone.txt\n@@ -1,1 +1,1 @@\n-x\n+y\n",
			wantErr:     true,
			want:        map[string]string{"/work/a.txt": "one\n"},
			wantNoStdio: true,
		},
		{
			name:  "a later deletion of a missing target leaves the earlier file untouched",
			files: map[string]string{"/work/a.txt": "one\n"},
			diff: "--- a.txt\n+++ a.txt\n@@ -1,1 +1,1 @@\n-one\n+ONE\n" +
				"--- gone.txt\n+++ /dev/null\n@@ -1,1 +0,0 @@\n-x\n",
			wantErr:     true,
			want:        map[string]string{"/work/a.txt": "one\n"},
			wantNoStdio: true,
		},
		{
			name:  "a later creation over an existing hunk mismatch leaves nothing behind",
			files: map[string]string{"/work/a.txt": "one\n"},
			diff: "--- /dev/null\n+++ new.txt\n@@ -0,0 +1,1 @@\n+fresh\n" +
				"--- a.txt\n+++ a.txt\n@@ -1,1 +1,1 @@\n-nope\n+ONE\n",
			wantErr:     true,
			want:        map[string]string{"/work/a.txt": "one\n"},
			absent:      []string{"/work/new.txt"},
			wantNoStdio: true,
		},
		{
			name:  "successive diffs for the same file build on each other",
			files: map[string]string{"/work/f": "a\n"},
			diff: "--- f\n+++ f\n@@ -1,1 +1,1 @@\n-a\n+A\n" +
				"--- f\n+++ f\n@@ -1,1 +1,1 @@\n-A\n+AA\n",
			want: map[string]string{"/work/f": "AA\n"},
		},
		{
			name:  "a deletion followed by a re-creation ends with the new content",
			files: map[string]string{"/work/f": "old\n"},
			diff: "--- f\n+++ /dev/null\n@@ -1,1 +0,0 @@\n-old\n" +
				"--- /dev/null\n+++ f\n@@ -0,0 +1,1 @@\n+new\n",
			want: map[string]string{"/work/f": "new\n"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env, stdout, _ := NewTestEnv(t, "/work")
			for p, content := range tt.files {
				mustWrite(t, env.FS, p, content)
			}
			env.Stdin = strings.NewReader(tt.diff)

			env.Args = tt.args
			err := patchCommand(context.Background(), env)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			for p, content := range tt.want {
				assert.Equal(t, content, mustRead(t, env.FS, p))
			}
			for _, p := range tt.absent {
				_, err := env.FS.Stat(p)
				assert.Errorf(t, err, "%s must not have been created", p)
			}
			if tt.wantNoStdio {
				assert.Empty(t, stdout.String(), "a rejected diff must not report a patched file")
			}
		})
	}
}

// Test_patch_keepsFilesWrittenBeforeAnIOFailure pins the boundary of what
// patchCommand promises. Whether a hunk applies is decided before anything is
// written, but whether the filesystem accepts a write is not: a creation under a
// denied path is refused only at the write itself, and the files written before
// it stay written. tar reports the same shape of outcome for extraction.
func Test_patch_keepsFilesWrittenBeforeAnIOFailure(t *testing.T) {
	t.Parallel()

	env, base, stdout, _ := NewTestEnvWithDeny(t, "/work", "patch", "**/secret.txt")
	mustWrite(t, base, "/work/a.txt", "one\n")

	// The first section is an ordinary modification; the second creates a file the
	// deny policy refuses. A creation reads nothing beforehand, so the refusal
	// surfaces only at the write itself.
	env.Stdin = strings.NewReader(
		"--- a.txt\n+++ a.txt\n@@ -1,1 +1,1 @@\n-one\n+ONE\n" +
			"--- /dev/null\n+++ secret.txt\n@@ -0,0 +1,1 @@\n+leaked\n")

	env.Args = nil
	require.Error(t, patchCommand(context.Background(), env))

	assert.Equal(t, "ONE\n", mustRead(t, env.FS, "/work/a.txt"),
		"the write that succeeded before the failure stays")
	assert.Equal(t, "patching file a.txt\n", stdout.String(),
		"only the file actually written is reported")

	_, err := base.Stat("/work/secret.txt")
	assert.Error(t, err, "the refused creation must not have reached the filesystem")
}

// Test_diff_patch_roundtrip proves diff's output applies cleanly with patch.
func Test_diff_patch_roundtrip(t *testing.T) {
	t.Parallel()

	env, stdout, _ := NewTestEnv(t, "/work")
	old := "alpha\nbeta\ngamma\ndelta\n"
	want := "alpha\nBETA\ngamma\nDELTA\nepsilon\n"
	mustWrite(t, env.FS, "/work/f", old)
	mustWrite(t, env.FS, "/work/a/f", old)
	mustWrite(t, env.FS, "/work/b/f", want)

	// diff exits 1 when files differ; that is expected, not an error here.
	env.Args = []string{"a/f", "b/f"}
	_ = diffCommand(context.Background(), env)

	env.Stdin = strings.NewReader(stdout.String())
	env.Args = []string{"-p1"}
	require.NoError(t, patchCommand(context.Background(), env))
	assert.Equal(t, want, mustRead(t, env.FS, "/work/f"))
}
