package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/mrtc0/sh/v3/interp"
	"github.com/mrtc0/sh/v3/syntax"

	"github.com/mrtc0/sbsh/sandbox/command"
	"github.com/mrtc0/sbsh/sandbox/exec"
	"github.com/mrtc0/sbsh/vfs"
)

// maxDepth is how far executions may nest before the sandbox refuses to go
// further. A command calling back into the sandbox is a loop the sandbox cannot
// see the end of — a command that invokes itself is a script away — so the depth
// is bounded rather than left to run until the process runs out of stack. The
// bound is generous: composing a handful of commands is the case this exists
// for, and nothing legitimate nests eight deep.
const maxDepth = 8

// nestedExecutor is the runtime behind [command.Invocation.RunNested]: one
// executor per invocation, holding what a child of that invocation inherits.
//
// It is deliberately not [Sandbox.Exec]. A nested run happens while the
// top-level run that dispatched the command is still in progress, holding the
// sandbox's shell session; re-entering Exec would deadlock on that session, and
// running the child in it would let the child's variables and working directory
// leak into the caller's. A child gets a shell session of its own instead,
// started from the caller's directory and environment and discarded when it
// ends. Everything outside the session — the mounts, the deny patterns, the
// network policy, the registered commands — is the sandbox's and is therefore
// shared, which is what keeps a child on the same capability boundary.
type nestedExecutor struct {
	sandbox *Sandbox

	// parent is the execution the calling command is part of. Each child hangs
	// from it in the execution tree.
	parent exec.Execution

	// dir and env are what the calling command stands in: a request that names
	// neither runs where the caller does, with what the caller sees.
	dir string
	env []string
}

// nested builds the executor for one invocation. It is what
// [builtins.Options.Nested] is set to, and it is called after the invocation is
// built so that a child starts from where the command stands.
func (s *Sandbox) nested(ctx context.Context, inv *command.Invocation) command.NestedExecutor {
	// The execution is installed by Exec, so it is always there in practice. A
	// missing one is an invocation built outside the sandbox; treating it as a
	// root is what keeps the depth guard meaningful rather than absent.
	parent, _ := exec.FromContext(ctx)

	env := s.env
	if inv.Env != nil {
		env = inv.Env.All()
	}
	dir := inv.Dir
	if dir == "" {
		dir = "/"
	}
	return &nestedExecutor{sandbox: s, parent: parent, dir: dir, env: env}
}

// Run evaluates one nested request. It reports back through [exec.Finish], the
// same way [Sandbox.Exec] does, so a caller cannot tell a child's result from a
// top-level one by its shape.
func (e *nestedExecutor) Run(ctx context.Context, req command.NestedRequest) (*exec.Result, error) {
	if e.parent.Depth+1 > maxDepth {
		return exec.Denied(fmt.Sprintf("nested execution is %d levels deep, which is as far as the sandbox goes", maxDepth)), nil
	}
	child := e.parent.Child(e.sandbox.executions.Add(1))

	file, err := syntax.NewParser().Parse(strings.NewReader(req.Script), "nested")
	if err != nil {
		return exec.Invalid(fmt.Errorf("sandbox: nested parse: %w", err))
	}
	if err := quarantine(file); err != nil {
		return exec.Denied(err.Error()), nil
	}

	dir, res, err := e.childDir(req.Dir)
	if res != nil || err != nil {
		return res, err
	}
	env, err := childEnv(e.env, req.Env)
	if err != nil {
		return exec.Invalid(fmt.Errorf("sandbox: nested environment: %w", err))
	}

	// A limit only ever tightens: a request asking for more than the sandbox
	// allows gets the sandbox's.
	limit := e.sandbox.outputLimit
	if req.OutputLimit > 0 && req.OutputLimit < limit {
		limit = req.OutputLimit
	}
	stdout, stderr := newCapWriter(limit), newCapWriter(limit)

	// A session of the child's own, not the sandbox's: fresh variables and
	// functions, its own captured output, and nothing it changes reaching the
	// caller. The child reads no input; a request is a script, not a filter.
	runner, err := e.sandbox.newRunner(dir, env, interp.StdIO(strings.NewReader(""), stdout, stderr))
	if err != nil {
		return exec.Internal(fmt.Errorf("sandbox: nested interpreter: %w", err))
	}

	// The child's context carries the child's own place in the tree, so a
	// command the child dispatches nests from here rather than from the caller.
	// A requested timeout only ever tightens: deriving it from the caller's
	// context keeps the caller's deadline in force, and the earlier of the two
	// is what stops the run.
	ctx = exec.NewContext(ctx, child)
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	runErr := runner.Run(ctx, file)
	out := exec.Output{
		Stdout:    stdout.String(),
		Stderr:    stderr.String(),
		Truncated: stdout.Truncated() || stderr.Truncated(),
	}
	res, err = exec.Finish(ctx, out, runErr)
	if err != nil {
		return res, fmt.Errorf("sandbox: nested run script: %w", err)
	}
	return res, nil
}

// childDir resolves where the child starts. A non-nil result means the request
// is answered without running: the sandbox refuses a directory its policy covers
// the way it refuses anything else, and a directory that is not there is a
// request that could never become a run.
func (e *nestedExecutor) childDir(reqDir string) (string, *exec.Result, error) {
	if reqDir == "" {
		return e.dir, nil, nil
	}
	dir := reqDir
	if !strings.HasPrefix(dir, "/") {
		dir = e.dir + "/" + dir
	}
	dir = vfs.Normalize(dir)

	info, err := e.sandbox.fs.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrPermission):
		res := exec.Denied(fmt.Sprintf("nested execution in %s: permission denied", dir))
		return "", res, nil
	case err != nil:
		res, err := exec.Invalid(fmt.Errorf("sandbox: nested working directory: %w", err))
		return "", res, err
	case !info.IsDir():
		res, err := exec.Invalid(fmt.Errorf("sandbox: nested working directory: %s is not a directory", dir))
		return "", res, err
	}
	return dir, nil, nil
}

// childEnv layers the request's variables on top of what the caller sees, so a
// request states what it cares about rather than the whole environment.
func childEnv(inherited, overrides []string) ([]string, error) {
	env := make([]string, 0, len(inherited)+len(overrides))
	env = append(env, inherited...)
	for _, kv := range overrides {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || name == "" {
			return nil, fmt.Errorf("%q is not a NAME=value pair", kv)
		}
		env = append(env, kv)
	}
	return env, nil
}
