package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mrtc0/sh/v3/expand"
	"github.com/mrtc0/sh/v3/interp"
	"github.com/mrtc0/sh/v3/syntax"
	"github.com/spf13/afero"

	"github.com/mrtc0/sbsh/netpolicy"
	"github.com/mrtc0/sbsh/sandbox/builtins"
	"github.com/mrtc0/sbsh/sandbox/exitcode"
	"github.com/mrtc0/sbsh/sandbox/python"
	"github.com/mrtc0/sbsh/vfs"
)

type Sandbox struct {
	// router resolves a path to the mount that backs it. Only construction uses
	// it directly: mounting and creating the default directories must not be
	// subject to the deny patterns, or a pattern could stop the sandbox from
	// setting itself up.
	router *vfs.VFS
	// fs is the filesystem everything else sees, i.e. router with the deny
	// patterns layered on top. Builtins, the shell handlers and the Python
	// bridge all receive this one handle, so the patterns are enforced in a
	// single place.
	fs          vfs.FS
	roots       []*vfs.HostFS
	denyPaths   []string
	env         []string
	timeout     time.Duration
	outputLimit int64
	netAllow    []string
	httpClient  *http.Client
	pyInterp    *python.WazeroInterpreter

	// mu serializes Exec, which shares runner across calls.
	mu     sync.Mutex
	runner *interp.Runner
}

type Option func(*Sandbox) error

func WithHostMountRO(hostDir, virtualPath string) Option {
	return withHostMount(hostDir, virtualPath, true)
}

func WithHostMountRW(hostDir, virtualPath string) Option {
	return withHostMount(hostDir, virtualPath, false)
}

func WithMountRO(virtualPath string, fsys afero.Fs) Option {
	return func(s *Sandbox) error {
		return s.router.Mount(virtualPath, vfs.NewReadOnlyFS(fsys))
	}
}

func WithMountRW(virtualPath string, fsys afero.Fs) Option {
	return func(s *Sandbox) error {
		return s.router.Mount(virtualPath, fsys)
	}
}

func withHostMount(hostDir, virtualPath string, readonly bool) Option {
	return func(s *Sandbox) error {
		root, err := vfs.NewHostFS(hostDir)
		if err != nil {
			return fmt.Errorf("mounting %s: %w", hostDir, err)
		}
		var fsys afero.Fs = root
		if readonly {
			fsys = vfs.NewReadOnlyFS(root)
		}
		if err := s.router.Mount(virtualPath, fsys); err != nil {
			root.Close()
			return err
		}
		s.roots = append(s.roots, root)
		return nil
	}
}

// WithDenyPaths refuses every operation on the paths the patterns select, on
// top of whatever the mounts already allow. See [vfs.Pattern] for the syntax;
// in short, "*" stays within one segment, "**" spans any number of them, and a
// pattern that does not start with "/" applies at any depth. Denying a
// directory denies everything below it.
//
// The paths stay visible in directory listings; only access to them fails, with
// EACCES.
func WithDenyPaths(patterns ...string) Option {
	return func(s *Sandbox) error {
		s.denyPaths = append(s.denyPaths, patterns...)
		return nil
	}
}

func WithEnv(k, v string) Option {
	return func(s *Sandbox) error {
		s.env = append(s.env, fmt.Sprintf("%s=%s", k, v))
		return nil
	}
}

func WithTimeout(d time.Duration) Option {
	return func(s *Sandbox) error {
		s.timeout = d
		return nil
	}
}

func WithOutputLimit(n int64) Option {
	return func(s *Sandbox) error {
		s.outputLimit = n
		return nil
	}
}

// WithNetworkAllow lets sandboxed commands reach the destinations the entries
// describe. An entry is a host name ("example.com"), a host name with a leading
// wildcard ("*.github.com", which covers subdomains but not the domain itself),
// an IP address, or a CIDR block.
//
// A name entry grants whatever that name resolves to, a loopback or private
// address included; an address entry is what pins a destination. A name no entry
// covers is still reachable when the address it resolves to falls inside an
// address entry, and the connection is opened to the address that was checked.
//
// Without this option the sandbox has no network access at all.
func WithNetworkAllow(entries ...string) Option {
	return func(s *Sandbox) error {
		s.netAllow = append(s.netAllow, entries...)
		return nil
	}
}

func withPythonInterpreter(ctx context.Context) Option {
	return func(s *Sandbox) error {
		pyInterp, pyMount, err := newPythonInterpreter(ctx)
		if err != nil {
			return fmt.Errorf("failed to create python interpreter: %w", err)
		}
		s.pyInterp = pyInterp
		if err := pyMount(s); err != nil {
			return fmt.Errorf("failed to mount python stdlib: %w", err)
		}
		return nil
	}
}

func New(ctx context.Context, opts ...Option) (*Sandbox, error) {
	s := &Sandbox{
		router:      vfs.NewVFS(afero.NewMemMapFs()),
		env:         []string{"PATH=/bin", "HOME=/home/agent", "PWD=/"},
		timeout:     30 * time.Second,
		outputLimit: 4 << 20, // 4 MiB
	}

	opts = append(opts, withPythonInterpreter(ctx))

	for _, opt := range opts {
		if err := opt(s); err != nil {
			return nil, s.abort(fmt.Errorf("applying option: %w", err))
		}
	}

	if err := s.initFS(); err != nil {
		return nil, s.abort(fmt.Errorf("initializing FS: %w", err))
	}

	// Layering the deny patterns comes after the mounts and the default
	// directories are in place, and before anything that runs sandboxed code
	// gets a handle.
	if err := s.applyDenyPaths(); err != nil {
		return nil, s.abort(fmt.Errorf("applying deny paths: %w", err))
	}

	if err := s.applyNetworkPolicy(); err != nil {
		return nil, s.abort(fmt.Errorf("applying network policy: %w", err))
	}

	runner, err := s.newRunner()
	if err != nil {
		return nil, s.abort(fmt.Errorf("creating interpreter: %w", err))
	}
	s.runner = runner
	return s, nil
}

// abort releases the resources a half-built sandbox holds and returns the error
// that made construction fail, noting a Close failure only when there is one.
func (s *Sandbox) abort(err error) error {
	if closeErr := s.Close(); closeErr != nil {
		return fmt.Errorf("%w; close error: %v", err, closeErr)
	}
	return err
}

// newRunner builds the interpreter that every Exec reuses. Its mounts,
// environment and handlers are fixed for the sandbox's lifetime; only the
// standard streams change, and Exec sets those per call.
func (s *Sandbox) newRunner() (*interp.Runner, error) {
	return interp.New(
		interp.Dir("/"),
		interp.Env(expand.ListEnviron(s.env...)),
		interp.ExecHandlers(builtins.ExecMiddleware(s.fs, builtins.Options{HTTP: s.httpClient, Python: s.pyInterp})),
		interp.OpenHandler(openHandler(s.fs)),
		interp.ReadDirHandler2(readDirHandler(s.fs)),
		interp.StatHandler(statHandler(s.fs)),
	)
}

// initFS creates the default directories in the virtual filesystem.
func (s *Sandbox) initFS() error {
	for _, dir := range []string{"/home/agent", "/tmp"} {
		if err := s.router.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("creating default dir %q: %w", dir, err)
		}
	}
	return nil
}

// applyDenyPaths sets the filesystem handle the rest of the sandbox uses. With
// no patterns that is the router itself, so the common case costs nothing.
func (s *Sandbox) applyDenyPaths() error {
	if len(s.denyPaths) == 0 {
		s.fs = s.router
		return nil
	}
	deny, err := vfs.NewDenyFS(s.router, s.denyPaths)
	if err != nil {
		return err
	}
	s.fs = deny
	return nil
}

// applyNetworkPolicy builds the client the builtins are given. With no entries
// there is no client, which is what leaves the sandbox with no way out.
func (s *Sandbox) applyNetworkPolicy() error {
	if len(s.netAllow) == 0 {
		return nil
	}
	policy, err := netpolicy.New(s.netAllow)
	if err != nil {
		return err
	}
	s.httpClient = policy.HTTPClient()
	return nil
}

// FS returns the sandbox's filesystem as sandboxed code sees it, with the deny
// patterns already in force.
func (s *Sandbox) FS() vfs.FS { return s.fs }

func (s *Sandbox) Close() error {
	var errs []error
	if s.pyInterp != nil {
		if err := s.pyInterp.Close(context.Background()); err != nil {
			errs = append(errs, err)
		}
	}
	for _, r := range s.roots {
		if err := r.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// Result is the result of executing a script in the sandbox.
type Result struct {
	Stdout    string
	Stderr    string
	ExitCode  int
	Truncated bool
}

// Exec interprets and executes a shell script. stdin is the script's standard
// input; if nil, it will be an empty reader.
// The returned error indicates a problem with the sandbox itself,
// while script failures are represented by Result.ExitCode.
//
// A sandbox is a single shell session: the working directory, variables and
// functions a script leaves behind are still in effect for the next Exec.
// Calls are therefore serialized, and concurrent callers wait their turn.
func (s *Sandbox) Exec(ctx context.Context, script string, stdin io.Reader) (*Result, error) {
	file, err := syntax.NewParser().Parse(strings.NewReader(script), "script")
	if err != nil {
		return nil, fmt.Errorf("sandbox: parse: %w", err)
	}
	if err := quarantine(file); err != nil {
		return nil, err
	}

	if stdin == nil {
		stdin = strings.NewReader("")
	}
	stdout := newCapWriter(s.outputLimit)
	stderr := newCapWriter(s.outputLimit)

	s.mu.Lock()
	defer s.mu.Unlock()

	// Applying StdIO to a runner that has already run is how the interpreter
	// expects streams to be swapped; see the [interp.Runner.Subshell] docs.
	if err := interp.StdIO(stdin, stdout, stderr)(s.runner); err != nil {
		return nil, fmt.Errorf("sandbox: standard streams: %w", err)
	}

	if s.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.timeout)
		defer cancel()
	}

	res := &Result{}
	runErr := s.runner.Run(ctx, file)
	res.Stdout = stdout.String()
	res.Stderr = stderr.String()
	res.Truncated = stdout.Truncated() || stderr.Truncated()
	if runErr == nil {
		return res, nil
	}

	var exitStatus interp.ExitStatus
	if errors.As(runErr, &exitStatus) {
		res.ExitCode = int(exitStatus)
		return res, nil
	}

	// Neither the timeout nor a cancellation is a failure of the sandbox: both are
	// limits the caller asked for, so they belong in ExitCode with a nil error, the
	// same way a non-zero script exit does.
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.ExitCode = exitcode.Timeout
		return res, nil
	case errors.Is(ctx.Err(), context.Canceled):
		res.ExitCode = exitcode.Canceled
		return res, nil
	}

	return res, fmt.Errorf("failed to run script: %w", runErr)
}

// quarantine rejects shell syntax that relies on host resources.
func quarantine(file *syntax.File) error {
	var err error
	syntax.Walk(file, func(node syntax.Node) bool {
		if _, ok := node.(*syntax.ProcSubst); ok {
			err = errors.New("process substitution is not allowed in the sandbox")
			return false
		}
		return true
	})
	return err
}

// capWriter is a buffer with an upper limit. Data exceeding the limit is silently discarded.
// Since interp may write concurrently from multiple goroutines in background jobs or pipelines,
// access is protected by mu.
type capWriter struct {
	mu        sync.Mutex
	buf       strings.Builder
	limit     int64
	truncated bool
}

func newCapWriter(limit int64) *capWriter { return &capWriter{limit: limit} }

func (w *capWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	remain := w.limit - int64(w.buf.Len())
	if remain <= 0 {
		w.truncated = true
		return len(p), nil
	}
	if int64(len(p)) > remain {
		w.buf.Write(p[:remain])
		w.truncated = true
		return len(p), nil
	}
	w.buf.Write(p)
	return len(p), nil
}

func (w *capWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func (w *capWriter) Truncated() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.truncated
}
