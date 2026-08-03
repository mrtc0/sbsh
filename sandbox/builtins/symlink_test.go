package builtins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrtc0/sbsh/sandbox/command"
)

// The recursive commands share one rule: a symbolic link found while walking is an
// entry in its own right, never a way into the tree it points at. GNU's commands
// work that way, and the alternative lets a command act on files outside the
// argument it was given. The cases live together because the rule is one rule.

// seedLinkTree lays out a tree holding every link shape a walk meets: one to a
// file, one to a directory, one whose target is outside the mount, and one pointing
// at a sibling directory that a walk must not enter.
//
//	work/a.txt          "match"
//	work/sub/b.txt      "match"
//	work/sub/blink   -> b.txt
//	work/other/c.txt    "match"
//	work/filelink    -> a.txt
//	work/dirlink     -> sub
//	work/uplink      -> other
//	work/abs         -> an absolute path outside the mount
func seedLinkTree(t *testing.T, hostDir string) {
	t.Helper()

	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("match"), 0644))

	for _, dir := range []string{"sub", "other"} {
		require.NoError(t, os.MkdirAll(filepath.Join(hostDir, dir), 0755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(hostDir, "a.txt"), []byte("match\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(hostDir, "sub/b.txt"), []byte("match\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(hostDir, "other/c.txt"), []byte("match\n"), 0644))

	for name, target := range map[string]string{
		"filelink":  "a.txt",
		"dirlink":   "sub",
		"uplink":    "other",
		"abs":       filepath.Join(outside, "secret.txt"),
		"sub/blink": "b.txt",
	} {
		require.NoError(t, os.Symlink(target, filepath.Join(hostDir, name)))
	}
}

// find reports a link as an entry and stops there. An absolute link is reported
// too: lstat describes the link without following it, so a link whose target the
// mount cannot reach no longer ends the walk.
func TestFind_DoesNotFollowSymlinks(t *testing.T) {
	t.Parallel()

	inv, hostDir, stdout, stderr := NewTestEnvWithHostMount(t, "find")
	seedLinkTree(t, hostDir)

	inv.Args = []string{"/work"}
	require.NoError(t, find(context.Background(), inv))

	got := strings.Fields(stdout.String())
	assert.ElementsMatch(t, []string{
		"/work",
		"/work/a.txt",
		"/work/abs",
		"/work/dirlink",
		"/work/filelink",
		"/work/other",
		"/work/other/c.txt",
		"/work/sub",
		"/work/sub/b.txt",
		"/work/sub/blink",
		"/work/uplink",
	}, got)
	assert.Empty(t, stderr.String())
}

// rm -r removes the link and leaves what it points to, which is what unlink(2)
// does for a link named directly. Reaching the same link through a walk must not
// change the answer.
func TestRm_DoesNotFollowSymlinks(t *testing.T) {
	t.Parallel()

	inv, hostDir, _, stderr := NewTestEnvWithHostMount(t, "rm")
	seedLinkTree(t, hostDir)

	inv.Args = []string{"-r", "/work/dirlink"}
	require.NoError(t, rm(context.Background(), inv))
	assert.NoFileExists(t, filepath.Join(hostDir, "dirlink"))
	assert.FileExists(t, filepath.Join(hostDir, "sub/b.txt"), "the link's target must survive")

	inv.Args = []string{"-r", "/work/other"}
	require.NoError(t, rm(context.Background(), inv))
	assert.NoFileExists(t, filepath.Join(hostDir, "other/c.txt"))
	assert.FileExists(t, filepath.Join(hostDir, "uplink"), "a link to a removed directory stays")

	assert.Empty(t, stderr.String())
}

// grep -r skips a link it meets while walking, as GNU grep does, and says nothing
// about it. Reading through the link would report the same file twice, and for a
// link pointing outside the mount it would fail on an entry the user never named.
func TestGrep_DoesNotFollowSymlinks(t *testing.T) {
	t.Parallel()

	inv, hostDir, stdout, stderr := NewTestEnvWithHostMount(t, "grep")
	seedLinkTree(t, hostDir)

	inv.Args = []string{"-r", "match", "/work"}
	require.NoError(t, grep(context.Background(), inv))

	got := strings.Fields(stdout.String())
	assert.ElementsMatch(t, []string{
		"/work/a.txt:match",
		"/work/other/c.txt:match",
		"/work/sub/b.txt:match",
	}, got)
	assert.Empty(t, stderr.String())
}

// cp -r cannot reproduce a link, because the virtual filesystem has no way to
// create one. It says so for that entry and copies the rest, rather than writing a
// copy of the target under the link's name.
func TestCp_ReportsSymlinkItCannotCopy(t *testing.T) {
	t.Parallel()

	inv, hostDir, _, stderr := NewTestEnvWithHostMount(t, "cp")
	seedLinkTree(t, hostDir)
	ctx := context.Background()

	inv.Args = []string{"-r", "/work/other", "/work/dst"}
	require.NoError(t, cp(ctx, inv),
		"a tree with no link copies cleanly")
	assert.FileExists(t, filepath.Join(hostDir, "dst/c.txt"))

	inv.Args = []string{"-r", "/work/sub", "/work/dst2"}
	err := cp(ctx, inv)
	require.Error(t, err)
	assert.Equal(t, &command.ExitError{Code: 1}, err)
	assert.Contains(t, stderr.String(), "cp: /work/sub/blink: symbolic link not copied")
	assert.FileExists(t, filepath.Join(hostDir, "dst2/b.txt"), "the rest of the tree is copied")
	assert.NoFileExists(t, filepath.Join(hostDir, "dst2/blink"))
}

// A source named as an argument is followed, which is what cp does with everything
// but -P: the copy holds what the link points at.
func TestCp_FollowsSymlinkGivenAsArgument(t *testing.T) {
	t.Parallel()

	inv, hostDir, _, stderr := NewTestEnvWithHostMount(t, "cp")
	seedLinkTree(t, hostDir)

	inv.Args = []string{"-r", "/work/uplink", "/work/dst"}
	require.NoError(t, cp(context.Background(), inv))
	assert.FileExists(t, filepath.Join(hostDir, "dst/c.txt"))
	assert.Empty(t, stderr.String())
}

// A destination inside the source would never end: the walk reads a directory's
// entries after the copy has created one there, so it keeps finding more to copy.
func TestCp_RefusesToCopyIntoItself(t *testing.T) {
	t.Parallel()

	inv, hostDir, _, _ := NewTestEnvWithHostMount(t, "cp")
	seedLinkTree(t, hostDir)

	inv.Args = []string{"-r", "/work/sub", "/work/sub/inner"}
	err := cp(context.Background(), inv)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "into itself")
}

// tar -c cannot store a link as a link for the same reason, so it reports the
// member and archives the rest.
func TestTar_ReportsSymlinkItCannotStore(t *testing.T) {
	t.Parallel()

	inv, hostDir, stdout, stderr := NewTestEnvWithHostMount(t, "tar")
	seedLinkTree(t, hostDir)

	inv.Args = []string{"-cf", "/work/a.tar", "sub", "dirlink"}
	err := tarCommand(context.Background(), inv)
	require.Error(t, err)
	assert.Equal(t, &command.ExitError{Code: 2}, err)
	assert.Contains(t, stderr.String(), "tar: /work/dirlink: symbolic link not archived")
	assert.FileExists(t, filepath.Join(hostDir, "a.tar"))

	stdout.Reset()
	inv.Args = []string{"-tf", "/work/a.tar"}
	require.NoError(t, tarCommand(context.Background(), inv))
	assert.ElementsMatch(t, []string{"sub/", "sub/b.txt"}, strings.Fields(stdout.String()))
}
