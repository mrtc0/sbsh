package pywasm

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"
)

//go:embed dist/python.wasm
var Wasm []byte

// The "all:" prefix is required: without it go:embed skips names beginning with
// "_" or ".", and the standard library has 190 of them, __init__.py among them.
// Losing those silently would leave a tree that looks complete and imports
// nothing.
//
//go:embed all:dist/lib
var stdlib embed.FS

//go:embed dist/PYTHON_VERSION
var version string

func Version() string { return strings.TrimSpace(version) }

// Stdlib returns the Python standard library as a read-only tree. Its root is
// the directory that becomes /usr/lib/pythonX.Y in the sandbox, so "os.py" sits
// at the top level.
func Stdlib() (fs.FS, error) {
	sub, err := fs.Sub(stdlib, "dist/lib")
	if err != nil {
		return nil, fmt.Errorf("pywasm: open embedded stdlib: %w", err)
	}
	return sub, nil
}

// MajorMinor returns the major.minor version of the embedded Python interpreter.
// It returns an error if the version string is malformed.
func MajorMinor() (string, error) {
	v := Version()
	p := strings.SplitN(v, ".", 3)
	if len(p) < 2 || p[0] == "" || p[1] == "" {
		return "", fmt.Errorf("pywasm: malformed PYTHON_VERSION %q", v)
	}
	return p[0] + "." + p[1], nil
}
