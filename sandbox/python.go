package sandbox

import (
	"context"
	"fmt"

	"github.com/mrtc0/sbsh/pywasm"
	"github.com/mrtc0/sbsh/sandbox/python"
)

// newPythonInterpreter builds the sandbox's Python runtime, with the
// conventional library root offered to its startup hook. The root is baked into
// the standard library tree, which is this sandbox's own copy, so what a sandbox
// may import from cannot reach another sandbox or the host process.
func newPythonInterpreter(ctx context.Context) (*python.WazeroInterpreter, Option, error) {
	pyVersion, err := pywasm.MajorMinor()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get python version: %w", err)
	}
	stdlibSrc, err := pywasm.Stdlib()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open embedded stdlib: %w", err)
	}
	stdlibFS, err := python.NewStdlibFS(stdlibSrc, python.LibraryRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create stdlib FS: %w", err)
	}
	interp, err := python.NewWazeroInterpreter(ctx, python.Config{
		Wasm:       pywasm.Wasm,
		MajorMinor: pyVersion,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create python interpreter: %w", err)
	}
	// The stdlib is immutable at runtime: sitecustomize.py and site-packages are
	// already written into stdlibFS by NewStdlibFS, and PYTHONDONTWRITEBYTECODE=1
	// suppresses .pyc writes. Mount it read-only so sandboxed code cannot rewrite
	// its own standard library.
	return interp, WithMountRO(python.LibPath(pyVersion), stdlibFS), nil
}
