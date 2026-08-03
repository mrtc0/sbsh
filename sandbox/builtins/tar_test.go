package builtins

import (
	"archive/tar"
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_tar_createListExtract(t *testing.T) {
	t.Parallel()

	env, stdout, _ := NewTestEnv(t, "/work")
	mustWrite(t, env.FS, "/work/src/a.txt", "alpha")
	mustWrite(t, env.FS, "/work/src/sub/b.txt", "bravo")

	// Create.
	env.Args = []string{"-cf", "out.tar", "src"}
	require.NoError(t, tarCommand(context.Background(), env))
	_, err := env.FS.Stat("/work/out.tar")
	require.NoError(t, err)

	// List.
	env.Args = []string{"-tf", "out.tar"}
	require.NoError(t, tarCommand(context.Background(), env))
	list := stdout.String()
	assert.Contains(t, list, "src/a.txt")
	assert.Contains(t, list, "src/sub/b.txt")

	// Extract into a fresh directory via -C.
	env2, _, _ := NewTestEnv(t, "/work")
	// Carry the archive over to the new filesystem.
	mustWrite(t, env2.FS, "/work/out.tar", mustRead(t, env.FS, "/work/out.tar"))
	require.NoError(t, env2.FS.MkdirAll("/work/dest", 0o755))
	env2.Args = []string{"-xf", "out.tar", "-C", "dest"}
	require.NoError(t, tarCommand(context.Background(), env2))

	assert.Equal(t, "alpha", mustRead(t, env2.FS, "/work/dest/src/a.txt"))
	assert.Equal(t, "bravo", mustRead(t, env2.FS, "/work/dest/src/sub/b.txt"))
}

func Test_tar_createFromCurrentDir(t *testing.T) {
	t.Parallel()

	env, stdout, _ := NewTestEnv(t, "/work")
	mustWrite(t, env.FS, "/work/src/a.txt", "alpha")
	mustWrite(t, env.FS, "/work/src/sub/b.txt", "bravo")

	env.Args = []string{"-cf", "out.tar", "."}
	require.NoError(t, tarCommand(context.Background(), env))
	env.Args = []string{"-tf", "out.tar"}
	require.NoError(t, tarCommand(context.Background(), env))

	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		assert.Falsef(t, strings.HasPrefix(line, "/"),
			"archive member %q must not be an absolute path", line)
	}
	list := stdout.String()
	assert.Contains(t, list, "./src/a.txt")
	assert.Contains(t, list, "./src/sub/b.txt")

	// Round-trips: extracting the "." archive restores the tree under -C.
	env2, _, _ := NewTestEnv(t, "/work")
	mustWrite(t, env2.FS, "/work/out.tar", mustRead(t, env.FS, "/work/out.tar"))
	require.NoError(t, env2.FS.MkdirAll("/work/dest", 0o755))
	env2.Args = []string{"-xf", "out.tar", "-C", "dest"}
	require.NoError(t, tarCommand(context.Background(), env2))
	assert.Equal(t, "alpha", mustRead(t, env2.FS, "/work/dest/src/a.txt"))
	assert.Equal(t, "bravo", mustRead(t, env2.FS, "/work/dest/src/sub/b.txt"))
}

func Test_tar_gzipRoundTrip(t *testing.T) {
	t.Parallel()

	env, _, _ := NewTestEnv(t, "/work")
	mustWrite(t, env.FS, "/work/f.txt", "compressed member")

	env.Args = []string{"-czf", "out.tgz", "f.txt"}
	require.NoError(t, tarCommand(context.Background(), env))
	env.Args = []string{"-xzf", "out.tgz", "-C", "restored"}
	require.NoError(t, tarCommand(context.Background(), env))

	assert.Equal(t, "compressed member", mustRead(t, env.FS, "/work/restored/f.txt"))
}

func Test_tar_verbose(t *testing.T) {
	t.Parallel()

	env, stdout, _ := NewTestEnv(t, "/work")
	mustWrite(t, env.FS, "/work/only.txt", "x")
	env.Args = []string{"-cvf", "out.tar", "only.txt"}
	require.NoError(t, tarCommand(context.Background(), env))
	assert.Contains(t, stdout.String(), "only.txt")
}

// hostileTar builds an uncompressed archive whose members carry the given names
// verbatim. It bypasses tarCreate, which sanitises names, so that a malicious
// archive can be produced.
func hostileTar(t *testing.T, names ...string) string {
	t.Helper()

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, name := range names {
		body := "payload of " + name
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		}))
		_, err := tw.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	return buf.String()
}

func Test_tar_extractConfinesMembers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		member  string
		args    []string
		want    string   // path the member must land on; empty when it is rejected
		absent  []string // paths that must not exist afterwards
		wantErr bool
	}{
		{
			name:    "parent escape is rejected",
			member:  "../../escape.txt",
			args:    []string{"-xf", "out.tar", "-C", "dest"},
			absent:  []string{"/escape.txt", "/work/escape.txt"},
			wantErr: true,
		},
		{
			name:    "escape hidden behind a prefix is rejected",
			member:  "sub/../../../escape.txt",
			args:    []string{"-xf", "out.tar", "-C", "dest"},
			absent:  []string{"/escape.txt", "/work/escape.txt"},
			wantErr: true,
		},
		{
			name:    "escape from the working directory is rejected",
			member:  "../escape.txt",
			args:    []string{"-xf", "out.tar"},
			absent:  []string{"/escape.txt"},
			wantErr: true,
		},
		{
			name:   "absolute member is stripped of its leading slash",
			member: "/abs.txt",
			args:   []string{"-xf", "out.tar", "-C", "dest"},
			want:   "/work/dest/abs.txt",
			absent: []string{"/abs.txt"},
		},
		{
			name:   "interior dotdot staying under base is kept",
			member: "a/../b.txt",
			args:   []string{"-xf", "out.tar", "-C", "dest"},
			want:   "/work/dest/b.txt",
		},
		{
			name:   "plain member lands under base",
			member: "sub/c.txt",
			args:   []string{"-xf", "out.tar", "-C", "dest"},
			want:   "/work/dest/sub/c.txt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env, _, _ := NewTestEnv(t, "/work")
			mustWrite(t, env.FS, "/work/out.tar", hostileTar(t, tt.member))
			require.NoError(t, env.FS.MkdirAll("/work/dest", 0o755))

			env.Args = tt.args
			err := tarCommand(context.Background(), env)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.member,
					"the error must name the offending member")
			} else {
				require.NoError(t, err)
				assert.Equal(t, "payload of "+tt.member, mustRead(t, env.FS, tt.want))
			}

			for _, p := range tt.absent {
				_, err := env.FS.Stat(p)
				assert.Errorf(t, err, "%s must not have been created", p)
			}
		})
	}
}

func Test_tar_extractKeepsMembersWrittenBeforeTheRejection(t *testing.T) {
	t.Parallel()

	env, _, _ := NewTestEnv(t, "/work")
	mustWrite(t, env.FS, "/work/out.tar", hostileTar(t, "ok.txt", "../escape.txt"))
	require.NoError(t, env.FS.MkdirAll("/work/dest", 0o755))

	env.Args = []string{"-xf", "out.tar", "-C", "dest"}
	require.Error(t, tarCommand(context.Background(), env))

	assert.Equal(t, "payload of ok.txt", mustRead(t, env.FS, "/work/dest/ok.txt"))
	_, err := env.FS.Stat("/work/escape.txt")
	assert.Error(t, err)
}

func Test_tar_errors(t *testing.T) {
	t.Parallel()

	t.Run("no mode", func(t *testing.T) {
		t.Parallel()
		env, _, _ := NewTestEnv(t, "/work")
		env.Args = []string{"-f", "out.tar"}
		require.Error(t, tarCommand(context.Background(), env))
	})

	t.Run("create without files", func(t *testing.T) {
		t.Parallel()
		env, _, _ := NewTestEnv(t, "/work")
		env.Args = []string{"-cf", "out.tar"}
		require.Error(t, tarCommand(context.Background(), env))
	})

	t.Run("unknown flag", func(t *testing.T) {
		t.Parallel()
		env, _, _ := NewTestEnv(t, "/work")
		env.Args = []string{"-cq", "out.tar", "x"}
		require.Error(t, tarCommand(context.Background(), env))
	})
}

// A denied member must not cost the whole archive. GNU tar warns, stores the rest,
// and exits 2, so the archive that lands is incomplete by report rather than by
// surprise.
func Test_tar_createContinuesPastDeniedEntries(t *testing.T) {
	t.Parallel()

	env, base, _, stderr := NewTestEnvWithDeny(t, "/work", "tar", "**/.env")
	mustWrite(t, base, "/work/src/.env", "SECRET")
	mustWrite(t, base, "/work/src/a.txt", "alpha")
	mustWrite(t, base, "/work/src/sub/b.txt", "bravo")

	env.Args = []string{"-cf", "out.tar", "src"}
	err := tarCommand(context.Background(), env)

	var ee exitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, 2, ee.code)
	assert.Contains(t, stderr.String(), "tar:")
	assert.Contains(t, stderr.String(), "permission denied")

	names := tarMemberNames(t, mustRead(t, base, "/work/out.tar"))
	assert.Contains(t, names, "src/a.txt")
	assert.Contains(t, names, "src/sub/b.txt")
	assert.NotContains(t, names, "src/.env")
}

func tarMemberNames(t *testing.T, archive string) []string {
	t.Helper()

	tr := tar.NewReader(bytes.NewReader([]byte(archive)))
	var names []string
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		names = append(names, hdr.Name)
	}
	return names
}
