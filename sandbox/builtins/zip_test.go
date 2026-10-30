package builtins

import (
	"archive/zip"
	"bytes"
	"context"
	"hash/crc32"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrtc0/sbsh/sandbox/command"
)

func Test_zip_createListExtract(t *testing.T) {
	t.Parallel()

	inv, stdout, _ := NewTestEnv(t, "/work")
	mustWrite(t, inv.FS, "/work/src/a.txt", "alpha")
	mustWrite(t, inv.FS, "/work/src/sub/b.txt", "bravo")

	inv.Args = []string{"-r", "out.zip", "src"}
	require.NoError(t, zipCommand(context.Background(), inv))
	assert.Equal(t, []string{"src/", "src/a.txt", "src/sub/", "src/sub/b.txt"},
		zipMemberNames(t, mustRead(t, inv.FS, "/work/out.zip")))
	assert.Contains(t, stdout.String(), "  adding: src/a.txt (stored 0%)")
	assert.Zero(t, mustZipReader(t, mustRead(t, inv.FS, "/work/out.zip")).File[0].UncompressedSize64,
		"a directory entry holds no data, whatever stat says its size is")

	// Extract into a fresh filesystem via -d, which is created on the way.
	inv2, stdout2, _ := NewTestEnv(t, "/work")
	mustWrite(t, inv2.FS, "/work/out.zip", mustRead(t, inv.FS, "/work/out.zip"))
	inv2.Args = []string{"-d", "dest", "out.zip"}
	require.NoError(t, unzipCommand(context.Background(), inv2))

	assert.Equal(t, "alpha", mustRead(t, inv2.FS, "/work/dest/src/a.txt"))
	assert.Equal(t, "bravo", mustRead(t, inv2.FS, "/work/dest/src/sub/b.txt"))
	assert.Contains(t, stdout2.String(), "Archive:  out.zip\n")
	assert.Contains(t, stdout2.String(), "   creating: src/sub/\n")
	assert.Contains(t, stdout2.String(), "  inflating: src/sub/b.txt\n")
}

func Test_zip_entryNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "a directory without -r is an entry of its own only",
			args: []string{"out.zip", "src"},
			want: []string{"src/"},
		},
		{
			name: "the working directory contributes its contents",
			args: []string{"-r", "out.zip", "."},
			want: []string{"src/", "src/a.txt", "src/sub/", "src/sub/b.txt"},
		},
		{
			name: "an absolute operand is stored relative",
			args: []string{"out.zip", "/work/src/a.txt"},
			want: []string{"work/src/a.txt"},
		},
		{
			name: "leading .. components are dropped",
			args: []string{"out.zip", "../work/src/a.txt"},
			want: []string{"work/src/a.txt"},
		},
		{
			name: "-j stores file names only",
			args: []string{"-rj", "out.zip", "src"},
			want: []string{"a.txt", "b.txt"},
		},
		{
			name: "a file named twice is stored once",
			args: []string{"out.zip", "src/a.txt", "./src/a.txt"},
			want: []string{"src/a.txt"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			inv, _, _ := NewTestEnv(t, "/work")
			mustWrite(t, inv.FS, "/work/src/a.txt", "alpha")
			mustWrite(t, inv.FS, "/work/src/sub/b.txt", "bravo")

			inv.Args = tt.args
			require.NoError(t, zipCommand(context.Background(), inv))
			assert.Equal(t, tt.want, zipMemberNames(t, mustRead(t, inv.FS, "/work/out.zip")))
		})
	}
}

func Test_zip_junkPathsCollisionIsAnError(t *testing.T) {
	t.Parallel()

	inv, _, _ := NewTestEnv(t, "/work")
	mustWrite(t, inv.FS, "/work/a/x.txt", "1")
	mustWrite(t, inv.FS, "/work/b/x.txt", "2")

	inv.Args = []string{"-j", "out.zip", "a/x.txt", "b/x.txt"}
	err := zipCommand(context.Background(), inv)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot repeat names")
	_, err = inv.FS.Stat("/work/out.zip")
	assert.Error(t, err, "no archive is written")
}

func Test_zip_compressesOnlyWhenItHelps(t *testing.T) {
	t.Parallel()

	inv, stdout, _ := NewTestEnv(t, "/work")
	mustWrite(t, inv.FS, "/work/big.txt", strings.Repeat("compressible ", 100))
	mustWrite(t, inv.FS, "/work/tiny.txt", "x")

	inv.Args = []string{"out.zip", "big.txt", "tiny.txt"}
	require.NoError(t, zipCommand(context.Background(), inv))

	zr := mustZipReader(t, mustRead(t, inv.FS, "/work/out.zip"))
	assert.Equal(t, zip.Deflate, zr.File[0].Method)
	assert.Equal(t, zip.Store, zr.File[1].Method)
	assert.Contains(t, stdout.String(), "  adding: big.txt (deflated ")
	assert.Contains(t, stdout.String(), "  adding: tiny.txt (stored 0%)")
}

func Test_zip_appendsZipSuffix(t *testing.T) {
	t.Parallel()

	inv, stdout, _ := NewTestEnv(t, "/work")
	mustWrite(t, inv.FS, "/work/a.txt", "alpha")

	inv.Args = []string{"-q", "archive", "a.txt"}
	require.NoError(t, zipCommand(context.Background(), inv))
	assert.Empty(t, stdout.String(), "-q prints nothing")
	assert.Equal(t, []string{"a.txt"}, zipMemberNames(t, mustRead(t, inv.FS, "/work/archive.zip")))

	// unzip finds the archive the same way.
	inv.Args = []string{"-p", "archive"}
	require.NoError(t, unzipCommand(context.Background(), inv))
	assert.Equal(t, "alpha", stdout.String())
}

func Test_zip_updatesExistingArchive(t *testing.T) {
	t.Parallel()

	inv, stdout, _ := NewTestEnv(t, "/work")
	mustWrite(t, inv.FS, "/work/a.txt", "old a")
	mustWrite(t, inv.FS, "/work/b.txt", "old b")
	inv.Args = []string{"out.zip", "a.txt", "b.txt"}
	require.NoError(t, zipCommand(context.Background(), inv))

	mustWrite(t, inv.FS, "/work/a.txt", "new a")
	mustWrite(t, inv.FS, "/work/c.txt", "c")
	stdout.Reset()
	inv.Args = []string{"out.zip", "c.txt", "a.txt"}
	require.NoError(t, zipCommand(context.Background(), inv))
	assert.Contains(t, stdout.String(), "  updating: a.txt")
	assert.Contains(t, stdout.String(), "  adding: c.txt")

	archive := mustRead(t, inv.FS, "/work/out.zip")
	assert.Equal(t, []string{"a.txt", "b.txt", "c.txt"}, zipMemberNames(t, archive),
		"a replaced entry keeps its place and a new one is appended")
	zr := mustZipReader(t, archive)
	assert.Equal(t, "new a", zipMemberBody(t, zr.File[0]))
	assert.Equal(t, "old b", zipMemberBody(t, zr.File[1]))
}

func Test_zip_refusesToOverwriteNonZip(t *testing.T) {
	t.Parallel()

	inv, _, _ := NewTestEnv(t, "/work")
	mustWrite(t, inv.FS, "/work/out.zip", "not a zip")
	mustWrite(t, inv.FS, "/work/a.txt", "alpha")

	inv.Args = []string{"out.zip", "a.txt"}
	require.Error(t, zipCommand(context.Background(), inv))
	assert.Equal(t, "not a zip", mustRead(t, inv.FS, "/work/out.zip"))
}

func Test_zip_skipsTheArchiveItself(t *testing.T) {
	t.Parallel()

	inv, _, _ := NewTestEnv(t, "/work")
	mustWrite(t, inv.FS, "/work/a.txt", "alpha")
	inv.Args = []string{"-r", "out.zip", "."}
	require.NoError(t, zipCommand(context.Background(), inv))
	// The second run walks a tree that now holds out.zip.
	require.NoError(t, zipCommand(context.Background(), inv))

	assert.Equal(t, []string{"a.txt"}, zipMemberNames(t, mustRead(t, inv.FS, "/work/out.zip")))
}

func Test_zip_toStdout(t *testing.T) {
	t.Parallel()

	inv, stdout, stderr := NewTestEnv(t, "/work")
	mustWrite(t, inv.FS, "/work/a.txt", "alpha")

	inv.Args = []string{"-", "a.txt"}
	require.NoError(t, zipCommand(context.Background(), inv))
	assert.Equal(t, []string{"a.txt"}, zipMemberNames(t, stdout.String()))
	assert.Contains(t, stderr.String(), "adding: a.txt", "progress moves out of the archive's way")
}

func Test_zip_missingOperand(t *testing.T) {
	t.Parallel()

	t.Run("the rest is archived and the status says it is incomplete", func(t *testing.T) {
		t.Parallel()

		inv, _, stderr := NewTestEnv(t, "/work")
		inv.Name = "zip"
		mustWrite(t, inv.FS, "/work/a.txt", "alpha")

		inv.Args = []string{"out.zip", "a.txt", "missing.txt"}
		err := zipCommand(context.Background(), inv)
		assert.Equal(t, &command.ExitError{Code: zipExitPartial}, err)
		assert.Contains(t, stderr.String(), "zip: name not matched: missing.txt")
		assert.Equal(t, []string{"a.txt"}, zipMemberNames(t, mustRead(t, inv.FS, "/work/out.zip")))
	})

	t.Run("nothing to archive", func(t *testing.T) {
		t.Parallel()

		inv, _, _ := NewTestEnv(t, "/work")
		inv.Args = []string{"out.zip", "missing.txt"}
		err := zipCommand(context.Background(), inv)
		var ee *command.ExitError
		require.ErrorAs(t, err, &ee)
		assert.Equal(t, zipExitNothingToDo, ee.Code)
		_, err = inv.FS.Stat("/work/out.zip")
		assert.Error(t, err)
	})
}

// A denied entry is reported and the rest archived, the way tar handles it.
func Test_zip_createContinuesPastDeniedEntries(t *testing.T) {
	t.Parallel()

	inv, base, _, stderr := NewTestEnvWithDeny(t, "/work", "zip", "**/.env")
	mustWrite(t, base, "/work/src/.env", "SECRET")
	mustWrite(t, base, "/work/src/a.txt", "alpha")

	inv.Args = []string{"-r", "out.zip", "src"}
	err := zipCommand(context.Background(), inv)
	assert.Equal(t, &command.ExitError{Code: zipExitPartial}, err)
	assert.Contains(t, stderr.String(), "zip:")
	assert.Contains(t, stderr.String(), "permission denied")

	names := zipMemberNames(t, mustRead(t, base, "/work/out.zip"))
	assert.Equal(t, []string{"src/", "src/a.txt"}, names)
}

func Test_zip_errors(t *testing.T) {
	t.Parallel()

	for name, args := range map[string][]string{
		"no operands":  nil,
		"no files":     {"out.zip"},
		"unknown flag": {"-Z", "out.zip", "a.txt"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			inv, _, _ := NewTestEnv(t, "/work")
			inv.Args = args
			require.Error(t, zipCommand(context.Background(), inv))
		})
	}
}

// zipFixture describes one member of a hand-built archive. hostileZip writes it
// with CreateRaw so that nothing about it is sanitised or recomputed.
type zipFixture struct {
	name   string
	body   string
	mode   os.FileMode // 0 leaves the default
	method uint16
	flags  uint16
	crc    uint32 // 0 computes the real one
}

func hostileZip(t *testing.T, members ...zipFixture) string {
	t.Helper()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, m := range members {
		hdr := &zip.FileHeader{
			Name:               m.name,
			Method:             m.method,
			Flags:              m.flags,
			CRC32:              m.crc,
			CompressedSize64:   uint64(len(m.body)),
			UncompressedSize64: uint64(len(m.body)),
		}
		if m.mode != 0 {
			hdr.SetMode(m.mode)
		}
		if hdr.CRC32 == 0 {
			hdr.CRC32 = crc32.ChecksumIEEE([]byte(m.body))
		}
		w, err := zw.CreateRaw(hdr)
		require.NoError(t, err)
		_, err = io.WriteString(w, m.body)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.String()
}

func Test_unzip_confinesMembers(t *testing.T) {
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
			args:    []string{"-d", "dest", "in.zip"},
			absent:  []string{"/escape.txt", "/work/escape.txt"},
			wantErr: true,
		},
		{
			name:    "escape hidden behind a prefix is rejected",
			member:  "sub/../../../escape.txt",
			args:    []string{"-d", "dest", "in.zip"},
			absent:  []string{"/escape.txt", "/work/escape.txt"},
			wantErr: true,
		},
		{
			name:    "escape from the working directory is rejected",
			member:  "../escape.txt",
			args:    []string{"in.zip"},
			absent:  []string{"/escape.txt"},
			wantErr: true,
		},
		{
			name:   "absolute member is stripped of its leading slash",
			member: "/abs.txt",
			args:   []string{"-d", "dest", "in.zip"},
			want:   "/work/dest/abs.txt",
			absent: []string{"/abs.txt"},
		},
		{
			name:   "interior dotdot staying under base is kept",
			member: "a/../b.txt",
			args:   []string{"-d", "dest", "in.zip"},
			want:   "/work/dest/b.txt",
		},
		{
			// A backslash is an ordinary character in a sandbox path, so this
			// is a single oddly named file rather than a way up.
			name:   "backslashes do not separate components",
			member: `..\escape.txt`,
			args:   []string{"-d", "dest", "in.zip"},
			want:   `/work/dest/..\escape.txt`,
			absent: []string{"/work/escape.txt"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			inv, _, _ := NewTestEnv(t, "/work")
			mustWrite(t, inv.FS, "/work/in.zip", hostileZip(t, zipFixture{name: tt.member, body: "payload"}))

			inv.Args = tt.args
			err := unzipCommand(context.Background(), inv)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.member, "the error must name the offending member")
			} else {
				require.NoError(t, err)
				assert.Equal(t, "payload", mustRead(t, inv.FS, tt.want))
			}
			for _, p := range tt.absent {
				_, err := inv.FS.Stat(p)
				assert.Errorf(t, err, "%s must not have been created", p)
			}
		})
	}
}

// Unlike tar, unzip sees every name before it writes, so one bad member means
// nothing is extracted at all.
func Test_unzip_rejectsTheWholeArchiveBeforeWriting(t *testing.T) {
	t.Parallel()

	tests := map[string]zipFixture{
		"escaping member":    {name: "../escape.txt", body: "x"},
		"encrypted member":   {name: "secret.txt", body: "x", flags: 0x1},
		"unsupported method": {name: "bzip2.txt", body: "x", method: 12},
	}
	for name, bad := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			inv, _, _ := NewTestEnv(t, "/work")
			mustWrite(t, inv.FS, "/work/in.zip", hostileZip(t,
				zipFixture{name: "ok.txt", body: "fine"}, bad))

			inv.Args = []string{"-d", "dest", "in.zip"}
			err := unzipCommand(context.Background(), inv)
			require.Error(t, err)
			assert.Contains(t, err.Error(), bad.name)
			_, err = inv.FS.Stat("/work/dest")
			assert.Error(t, err, "nothing may be written, not even the members before the bad one")
		})
	}
}

func Test_unzip_existingFiles(t *testing.T) {
	t.Parallel()

	archive := hostileZip(t,
		zipFixture{name: "new.txt", body: "new"},
		zipFixture{name: "keep.txt", body: "from archive"})

	tests := []struct {
		name     string
		flags    []string
		wantErr  string
		wantKeep string
		wantNew  bool
	}{
		{name: "refused without -o or -n", wantErr: "use -o to overwrite or -n", wantKeep: "on disk"},
		{name: "-o overwrites", flags: []string{"-o"}, wantKeep: "from archive", wantNew: true},
		{name: "-n keeps", flags: []string{"-n"}, wantKeep: "on disk", wantNew: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			inv, _, _ := NewTestEnv(t, "/work")
			mustWrite(t, inv.FS, "/work/in.zip", archive)
			mustWrite(t, inv.FS, "/work/keep.txt", "on disk")

			inv.Args = append(tt.flags, "in.zip")
			err := unzipCommand(context.Background(), inv)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantKeep, mustRead(t, inv.FS, "/work/keep.txt"))
			_, err = inv.FS.Stat("/work/new.txt")
			assert.Equal(t, tt.wantNew, err == nil, "new.txt extracted")
		})
	}
}

// Two members landing on one path would have the second silently replace the
// first, so they are treated as a conflict like a file already on disk.
func Test_unzip_duplicateTargetsConflict(t *testing.T) {
	t.Parallel()

	inv, _, _ := NewTestEnv(t, "/work")
	mustWrite(t, inv.FS, "/work/in.zip", hostileZip(t,
		zipFixture{name: "a.txt", body: "first"},
		zipFixture{name: "sub/../a.txt", body: "second"}))

	inv.Args = []string{"in.zip"}
	require.Error(t, unzipCommand(context.Background(), inv))
	_, err := inv.FS.Stat("/work/a.txt")
	assert.Error(t, err)

	inv.Args = []string{"-o", "in.zip"}
	require.NoError(t, unzipCommand(context.Background(), inv))
	assert.Equal(t, "second", mustRead(t, inv.FS, "/work/a.txt"))
}

func Test_unzip_skipsSymlinks(t *testing.T) {
	t.Parallel()

	inv, _, stderr := NewTestEnv(t, "/work")
	inv.Name = "unzip"
	mustWrite(t, inv.FS, "/work/in.zip", hostileZip(t,
		zipFixture{name: "link", body: "/etc/passwd", mode: os.ModeSymlink | 0o777},
		zipFixture{name: "a.txt", body: "alpha"}))

	inv.Args = []string{"in.zip"}
	err := unzipCommand(context.Background(), inv)
	assert.Equal(t, &command.ExitError{Code: unzipExitWarning}, err)
	assert.Contains(t, stderr.String(), "unzip: link: symbolic link not extracted")
	assert.Equal(t, "alpha", mustRead(t, inv.FS, "/work/a.txt"))
	_, err = inv.FS.Stat("/work/link")
	assert.Error(t, err)
}

func Test_unzip_permissions(t *testing.T) {
	t.Parallel()

	inv, _, _ := NewTestEnv(t, "/work")
	mustWrite(t, inv.FS, "/work/in.zip", hostileZip(t,
		zipFixture{name: "run.sh", body: "#!/bin/sh", mode: os.ModeSetuid | 0o755}))

	inv.Args = []string{"in.zip"}
	require.NoError(t, unzipCommand(context.Background(), inv))
	info, err := inv.FS.Stat("/work/run.sh")
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode()&(os.ModePerm|os.ModeSetuid))
}

func Test_unzip_checksumMismatchFails(t *testing.T) {
	t.Parallel()

	inv, _, _ := NewTestEnv(t, "/work")
	mustWrite(t, inv.FS, "/work/in.zip", hostileZip(t,
		zipFixture{name: "a.txt", body: "alpha", crc: 0xdeadbeef}))

	inv.Args = []string{"in.zip"}
	err := unzipCommand(context.Background(), inv)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checksum")
}

func Test_unzip_selectMembers(t *testing.T) {
	t.Parallel()

	archive := hostileZip(t,
		zipFixture{name: "docs/a.md", body: "a"},
		zipFixture{name: "docs/b.txt", body: "b"},
		zipFixture{name: "src/c.go", body: "c"})

	t.Run("patterns and exact names select members", func(t *testing.T) {
		t.Parallel()

		inv, _, _ := NewTestEnv(t, "/work")
		mustWrite(t, inv.FS, "/work/in.zip", archive)
		inv.Args = []string{"-q", "in.zip", "docs/*.md", "src/c.go"}
		require.NoError(t, unzipCommand(context.Background(), inv))

		assert.Equal(t, "a", mustRead(t, inv.FS, "/work/docs/a.md"))
		assert.Equal(t, "c", mustRead(t, inv.FS, "/work/src/c.go"))
		_, err := inv.FS.Stat("/work/docs/b.txt")
		assert.Error(t, err)
	})

	t.Run("an operand matching nothing exits 11", func(t *testing.T) {
		t.Parallel()

		inv, _, stderr := NewTestEnv(t, "/work")
		inv.Name = "unzip"
		mustWrite(t, inv.FS, "/work/in.zip", archive)
		inv.Args = []string{"-q", "in.zip", "src/c.go", "nope"}
		err := unzipCommand(context.Background(), inv)
		assert.Equal(t, &command.ExitError{Code: unzipExitNoMatch}, err)
		assert.Contains(t, stderr.String(), "unzip: filename not matched: nope")
		assert.Equal(t, "c", mustRead(t, inv.FS, "/work/src/c.go"))
	})
}

func Test_unzip_pipe(t *testing.T) {
	t.Parallel()

	inv, stdout, _ := NewTestEnv(t, "/work")
	mustWrite(t, inv.FS, "/work/in.zip", hostileZip(t,
		zipFixture{name: "dir/", mode: os.ModeDir | 0o755},
		zipFixture{name: "dir/a.txt", body: "alpha\n"},
		zipFixture{name: "dir/b.txt", body: "bravo\n"}))

	inv.Args = []string{"-p", "in.zip", "dir/b.txt"}
	require.NoError(t, unzipCommand(context.Background(), inv))
	assert.Equal(t, "bravo\n", stdout.String())

	stdout.Reset()
	inv.Args = []string{"-p", "in.zip"}
	require.NoError(t, unzipCommand(context.Background(), inv))
	assert.Equal(t, "alpha\nbravo\n", stdout.String())

	_, err := inv.FS.Stat("/work/dir")
	assert.Error(t, err, "-p writes nothing to the filesystem")
}

func Test_unzip_list(t *testing.T) {
	t.Parallel()

	inv, stdout, _ := NewTestEnv(t, "/work")
	mustWrite(t, inv.FS, "/work/a.txt", "alpha")
	mustWrite(t, inv.FS, "/work/b.txt", "bravo!")
	inv.Args = []string{"-q", "in.zip", "a.txt", "b.txt"}
	require.NoError(t, zipCommand(context.Background(), inv))

	inv.Args = []string{"-l", "in.zip"}
	require.NoError(t, unzipCommand(context.Background(), inv))

	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	require.Len(t, lines, 7)
	assert.Equal(t, "Archive:  in.zip", lines[0])
	assert.Equal(t, "  Length      Date    Time    Name", lines[1])
	assert.Regexp(t, `^        5  \d{4}-\d\d-\d\d \d\d:\d\d   a\.txt$`, lines[3])
	assert.Regexp(t, `^        6  \d{4}-\d\d-\d\d \d\d:\d\d   b\.txt$`, lines[4])
	assert.Equal(t, "       11                     2 files", lines[6])
}

func Test_unzip_errors(t *testing.T) {
	t.Parallel()

	tests := map[string][]string{
		"no archive":        nil,
		"missing archive":   {"missing.zip"},
		"not a zip":         {"plain.txt"},
		"list and pipe":     {"-l", "-p", "in.zip"},
		"overwrite and not": {"-o", "-n", "in.zip"},
		"unknown flag":      {"-Z", "in.zip"},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			inv, _, _ := NewTestEnv(t, "/work")
			mustWrite(t, inv.FS, "/work/plain.txt", "hello")
			mustWrite(t, inv.FS, "/work/in.zip", hostileZip(t, zipFixture{name: "a", body: "a"}))
			inv.Args = args
			require.Error(t, unzipCommand(context.Background(), inv))
		})
	}
}

func mustZipReader(t *testing.T, archive string) *zip.Reader {
	t.Helper()

	zr, err := zip.NewReader(strings.NewReader(archive), int64(len(archive)))
	require.NoError(t, err)
	return zr
}

func zipMemberNames(t *testing.T, archive string) []string {
	t.Helper()

	var names []string
	for _, f := range mustZipReader(t, archive).File {
		names = append(names, f.Name)
	}
	return names
}

func zipMemberBody(t *testing.T, f *zip.File) string {
	t.Helper()

	rc, err := f.Open()
	require.NoError(t, err)
	defer rc.Close()
	b, err := io.ReadAll(rc)
	require.NoError(t, err)
	return string(b)
}
