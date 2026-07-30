package python

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"

	"github.com/spf13/afero"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/experimental/sysfs"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"

	"github.com/mrtc0/sbsh/sandbox/exitcode"
	"github.com/mrtc0/sbsh/vfs"
)

const (
	PythonArgv0Env = "SBSH_PYTHON_ARGV0"
	PythonCwdEnv   = "SBSH_PYTHON_CWD"
)

// siteDir is the directory for site-packages, which is created empty in the stdlib FS.
const siteDir = "/site-packages"

// siteCustomizePy is the content of the sitecustomize.py file that is
// automatically imported by CPython during site initialization.
// It sets sys.argv[0] and the current working directory based on environment variables.
var sitecustomizePy = fmt.Sprintf(`import sys, os
_argv0 = os.environ.get('%s')
if _argv0 is not None:
    sys.argv[0] = _argv0
_cwd = os.environ.get('%s')
if _cwd:
    os.chdir(_cwd)
del _argv0, _cwd
`, PythonArgv0Env, PythonCwdEnv)

// Invocation represents a single invocation of the Python interpreter.
type Invocation struct {
	// Code is the Python code to execute, either from -c or from a script file.
	Code string
	// Argv is the command-line arguments passed to the Python interpreter.
	// Argv[0] is the script name or "-c" for code, and Argv[1:] are the additional arguments.
	Argv []string
	// Cwd is the current working directory for the Python process. Since WASI does not have
	// an initial cwd concept, sitecustomize will call os.chdir at startup.
	Cwd string
	// Env is the environment variables for the Python process, in "KEY=VALUE" format.
	Env []string
	// Stdin is the standard input for the Python process. If nil, it will be an empty reader.
	Stdin io.Reader
	// FS is the virtual file system for the Python process. It should be a read-only FS
	// containing the stdlib and any other necessary files. The FS is mounted at / in the WASI
	// environment, so the stdlib should be at /usr/lib/pythonX.Y where X.Y is the Python version.
	// It is a vfs.FS so that the WASI bridge can use it directly: wrapping an already-resolved
	// filesystem in another VFS would add a redundant resolve/normalize layer on every syscall.
	FS vfs.FS
}

// InvocationResult represents the result of a Python invocation.
type InvocationResult struct {
	Stdout   string
	Stderr   string
	ExitCode uint32
}

func (r InvocationResult) Ok() bool { return r.ExitCode == 0 }

// Interpreter is the interface for running Python code in a WASI environment.
// It abstracts over the details of the WASI runtime and the Python interpreter.
type Interpreter interface {
	// Run executes the given Invocation and returns the result.
	// The returned error indicates a problem with the interpreter itself, while
	// the program's termination — whether it exited on its own or was stopped by
	// the sandbox — is represented by InvocationResult.ExitCode.
	Run(ctx context.Context, inv Invocation) (InvocationResult, error)
}

// Config is the configuration for creating a WazeroInterpreter.
type Config struct {
	// Wasm is the bytes of the python.wasm module.
	Wasm []byte

	// MajorMinor is the Python version in "X.Y" format, e.g., "3.11".
	// It is used to determine the default PythonLib path if not explicitly set.
	MajorMinor string

	// PythonHome is the path to the Python home directory, defaulting to "/usr".
	PythonHome string
	// PythonLib is the path to the Python standard library, defaulting to "/usr/lib/python<MajorMinor>".
	PythonLib string
}

// WazeroInterpreter is an implementation of the Interpreter interface using the Wazero WASI runtime.
type WazeroInterpreter struct {
	runtime  wazero.Runtime
	compiled wazero.CompiledModule
	// home is the Python home directory, typically "/usr".
	home string
	// lib is the path to the Python standard library, typically "/usr/lib/pythonX.Y".
	lib string
}

var _ Interpreter = (*WazeroInterpreter)(nil)

func NewWazeroInterpreter(ctx context.Context, cfg Config) (*WazeroInterpreter, error) {
	home := cfg.PythonHome
	if home == "" {
		home = "/usr"
	}

	lib := cfg.PythonLib
	if lib == "" {
		if cfg.MajorMinor == "" {
			return nil, errors.New("python: Config needs MajorMinor or PythonLib")
		}
		lib = LibPath(cfg.MajorMinor)
	}

	rt := wazero.NewRuntimeWithConfig(ctx,
		wazero.NewRuntimeConfig().WithCloseOnContextDone(true))
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		rt.Close(ctx)
		return nil, fmt.Errorf("python: instantiate wasi_snapshot_preview1: %w", err)
	}

	compiled, err := rt.CompileModule(ctx, cfg.Wasm)
	if err != nil {
		rt.Close(ctx)
		return nil, fmt.Errorf("python: compile python.wasm: %w", err)
	}
	return &WazeroInterpreter{runtime: rt, compiled: compiled, home: home, lib: lib}, nil
}

// LibPath returns the default path to the Python standard library for the given major.minor version.
func LibPath(majorMinor string) string { return "/usr/lib/python" + majorMinor }

// NewStdlibFS creates a virtual filesystem containing the Python standard
// library copied from src, whose root is the directory that becomes
// /usr/lib/pythonX.Y. It also installs a sitecustomize.py file and creates an
// empty site-packages directory.
//
// The result is meant to be mounted read-only; the copy exists so that those two
// additions are possible at all.
func NewStdlibFS(src fs.FS) (afero.Fs, error) {
	tree, err := extractStdlib(src)
	if err != nil {
		return nil, fmt.Errorf("python: extract stdlib: %w", err)
	}
	if err := afero.WriteFile(tree, "/sitecustomize.py", []byte(sitecustomizePy), 0o644); err != nil {
		return nil, fmt.Errorf("python: install sitecustomize: %w", err)
	}
	if err := tree.MkdirAll(siteDir, 0o755); err != nil {
		return nil, fmt.Errorf("python: create site-packages: %w", err)
	}
	return tree, nil
}

func (e *WazeroInterpreter) Close(ctx context.Context) error { return e.runtime.Close(ctx) }

// Run executes the given Invocation in the Wazero WASI runtime and returns the result.
// The returned error indicates a problem with the interpreter itself, while the
// program's termination — whether it exited on its own or was stopped by the
// sandbox — is represented by InvocationResult.ExitCode.
func (e *WazeroInterpreter) Run(ctx context.Context, inv Invocation) (InvocationResult, error) {
	var out, errb bytes.Buffer

	stdin := inv.Stdin
	if stdin == nil {
		stdin = strings.NewReader("")
	}

	bridge := wasmFS{fs: inv.FS}
	fsCfg := wazero.NewFSConfig().(sysfs.FSConfig).WithSysFSMount(bridge, "/")

	wargs := []string{"python", "-c", inv.Code}
	if len(inv.Argv) > 1 {
		wargs = append(wargs, inv.Argv[1:]...)
	}
	mod := wazero.NewModuleConfig().
		WithFSConfig(fsCfg).
		WithName("").
		WithStdout(&out).WithStderr(&errb).WithStdin(stdin).
		WithArgs(wargs...)

	for _, kv := range inv.Env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			mod = mod.WithEnv(k, v)
		}
	}

	mod = mod.
		WithEnv("PYTHONHOME", e.home).
		WithEnv("PYTHONPATH", e.lib).
		WithEnv("PYTHONDONTWRITEBYTECODE", "1")
	if len(inv.Argv) > 0 {
		mod = mod.WithEnv(PythonArgv0Env, inv.Argv[0])
	}
	if inv.Cwd != "" {
		mod = mod.WithEnv(PythonCwdEnv, inv.Cwd)
	}

	instance, err := e.runtime.InstantiateModule(ctx, e.compiled, mod)
	if instance != nil {
		instance.Close(ctx)
	}

	res := InvocationResult{Stdout: out.String(), Stderr: errb.String()}

	// Checked before the sys.ExitError branch below: WithCloseOnContextDone makes
	// wazero report a context-driven shutdown as an ExitError carrying its own
	// sentinel code (sys.ExitCodeDeadlineExceeded / ExitCodeContextCanceled),
	// which would otherwise leak into ExitCode in place of 128 + signal.
	if ctxErr := ctx.Err(); ctxErr != nil {
		switch {
		case errors.Is(ctxErr, context.DeadlineExceeded):
			res.ExitCode = exitcode.Timeout
		default:
			res.ExitCode = exitcode.Canceled
		}
		return res, nil
	}

	var exit *sys.ExitError
	if errors.As(err, &exit) {
		res.ExitCode = exit.ExitCode()
		return res, nil
	}

	return res, err
}

// extractStdlib copies the standard library tree into a writable in-memory
// filesystem. The copy is what lets NewStdlibFS add sitecustomize.py and an
// empty site-packages to a tree that is embedded, and therefore read-only.
func extractStdlib(src fs.FS) (afero.Fs, error) {
	if src == nil {
		return nil, errors.New("nil stdlib source")
	}

	mem := afero.NewMemMapFs()
	found := false
	err := fs.WalkDir(src, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == "." {
			return nil
		}

		dst := "/" + name
		if d.IsDir() {
			return mem.MkdirAll(dst, 0o755)
		}
		if err := mem.MkdirAll(path.Dir(dst), 0o755); err != nil {
			return err
		}

		f, err := src.Open(name)
		if err != nil {
			return err
		}
		defer f.Close()

		found = true
		return afero.WriteReader(mem, dst, f)
	})
	if err != nil {
		return nil, err
	}
	// An embed pattern that matches nothing is not a build error, so an empty
	// tree reaches this far. Report it here rather than letting the interpreter
	// start with no importable module.
	if !found {
		return nil, errors.New("stdlib source contains no files")
	}
	return mem, nil
}
