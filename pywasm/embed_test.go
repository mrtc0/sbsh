package pywasm_test

import (
	"encoding/binary"
	"io/fs"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrtc0/sbsh/pywasm"
)

// These tests pin properties of the committed build artifacts from outside the
// Docker build that produces them. Whether pywasm/Dockerfile stripped the module
// or excluded the right directories is invisible from Go otherwise, so a change
// to the Dockerfile that quietly drops one of these steps would go unnoticed
// until someone cloned the repository.

// maxWasmSize is a ceiling, not a measurement: the stripped module is about
// 7.7 MiB, and anything near the unstripped 30 MiB means the strip step was
// skipped. It catches the regression even if the debug sections were removed
// under names this test does not know about.
const maxWasmSize = 12 << 20

func TestWasm_HasNoDebugSections(t *testing.T) {
	t.Parallel()

	for _, name := range wasmCustomSections(t, pywasm.Wasm) {
		assert.False(t, strings.HasPrefix(name, ".debug_"),
			"python.wasm carries the %q section: the Docker build must run llvm-strip --strip-debug", name)
	}
}

func TestWasm_Size(t *testing.T) {
	t.Parallel()

	assert.Less(t, len(pywasm.Wasm), maxWasmSize,
		"python.wasm is %d bytes; a stripped module is about 7.7 MiB", len(pywasm.Wasm))
}

func TestStdlib_HasSentinelFiles(t *testing.T) {
	t.Parallel()

	src, err := pywasm.Stdlib()
	require.NoError(t, err)

	// encodings is what a bare interpreter imports before anything else, so its
	// absence breaks startup rather than one module.
	//
	// The __init__.py entries double as the canary for the "all:" prefix on the
	// embed pattern: their names begin with "_", so without the prefix go:embed
	// skips every one of them and the tree still looks populated while importing
	// nothing.
	for _, name := range []string{
		"os.py",
		"re/__init__.py",
		"json/__init__.py",
		"encodings/__init__.py",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			b, err := fs.ReadFile(src, name)
			require.NoError(t, err)
			assert.NotEmpty(t, b)
		})
	}
}

func TestStdlib_HasNoBytecode(t *testing.T) {
	t.Parallel()

	src, err := pywasm.Stdlib()
	require.NoError(t, err)

	var found []string
	err = fs.WalkDir(src, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == "__pycache__" || strings.HasSuffix(name, ".pyc") {
			found = append(found, name)
		}
		return nil
	})
	require.NoError(t, err)

	// A timestamp-based .pyc is accepted only when it records its source's mtime,
	// and NewStdlibFS copies this tree into a fresh in-memory filesystem that
	// stamps every file at copy time. No committed .pyc can match, so CPython
	// rejects all of them and compiles from source anyway; shipping them costs
	// 4 MiB here and 3.5 MiB of churn per rebuild for nothing. Measured before
	// removing them: 135 present, 0 accepted.
	assert.Empty(t, found, "the Docker build must drop __pycache__: committed bytecode is never accepted at runtime")
}

func TestStdlib_HasNoTests(t *testing.T) {
	t.Parallel()

	src, err := pywasm.Stdlib()
	require.NoError(t, err)

	for _, name := range []string{"test", "tests", "idlelib", "turtledemo"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := fs.Stat(src, name)
			assert.ErrorIs(t, err, fs.ErrNotExist,
				"%q is excluded by the Docker build and must not reach the artifact", name)
		})
	}
}

// wasmCustomSections returns the names of the custom sections in a WebAssembly
// module, in the order they appear. It parses only the section headers: a section
// is a one-byte id followed by a LEB128 length and that many bytes of body, and
// a custom section (id 0) starts its body with a LEB128-prefixed name.
func wasmCustomSections(t *testing.T, mod []byte) []string {
	t.Helper()

	require.Greater(t, len(mod), 8, "too short to be a wasm module")
	require.Equal(t, []byte{0x00, 'a', 's', 'm'}, mod[:4], "not a wasm module")

	var names []string
	for i := 8; i < len(mod); {
		id := mod[i]
		i++

		size, n := binary.Uvarint(mod[i:])
		require.Positive(t, n, "malformed length for section id %d", id)
		i += n
		require.LessOrEqual(t, i+int(size), len(mod), "section id %d runs past the end", id)

		body := mod[i : i+int(size)]
		i += int(size)

		if id != 0 {
			continue
		}
		nameLen, n := binary.Uvarint(body)
		require.Positive(t, n, "malformed name length in custom section")
		require.LessOrEqual(t, n+int(nameLen), len(body), "custom section name runs past the section")
		names = append(names, string(body[n:n+int(nameLen)]))
	}
	return names
}
