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
// started where the caller stands, with only the environment the request asks
// for, and discarded when it ends. Everything outside the session — the mounts,
// the deny patterns, the network policy, the registered commands — is the
// sandbox's and is therefore shared, which is what keeps a child on the same
// capability boundary.
type nestedExecutor struct {
	sandbox *Sandbox

	// parent is the execution the calling command is part of. Each child hangs
	// from it in the execution tree.
	parent exec.Execution

	// dir is where the calling command stands: a request that names no directory
	// runs where the caller does.
	dir string

	// implicitEnv is the whole of what a child is given without asking for it.
	// See [buildChildEnv] for why it is this short.
	implicitEnv []string
}

// nested builds the executor for one invocation. It is what
// [builtins.Options.Nested] is set to, and it is called after the invocation is
// built so that a child starts from where the command stands.
func (s *Sandbox) nested(ctx context.Context, inv *command.Invocation) command.NestedExecutor {
	// The execution is installed by Exec, so it is always there in practice. A
	// missing one is an invocation built outside the sandbox; treating it as a
	// root is what keeps the depth guard meaningful rather than absent.
	parent, _ := exec.FromContext(ctx)

	dir := inv.Dir
	if dir == "" {
		dir = "/"
	}

	// HOME is where the user's things are, which is as true for a child as for
	// the caller, and a script has no other way to ask. Everything else the
	// caller happens to have is left behind; see [buildChildEnv].
	var implicitEnv []string
	if home, ok := lookupCallerEnv(s.env, inv.Env, "HOME"); ok {
		implicitEnv = append(implicitEnv, "HOME="+home)
	}
	return &nestedExecutor{sandbox: s, parent: parent, dir: dir, implicitEnv: implicitEnv}
}

// lookupCallerEnv reads one variable as the calling command sees it, falling
// back to the sandbox's own environment for an invocation built without one.
func lookupCallerEnv(sandboxEnv []string, inv command.Environ, name string) (string, bool) {
	if inv != nil {
		return inv.Lookup(name)
	}
	// Last duplicate wins, the same way expand.ListEnviron reads the slice.
	value, found := "", false
	for _, kv := range sandboxEnv {
		if n, v, ok := strings.Cut(kv, "="); ok && n == name {
			value, found = v, true
		}
	}
	return value, found
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
	env, err := buildChildEnv(e.implicitEnv, req.Env, dir)
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

// buildChildEnv assembles the environment a child runs with: implicitEnv, the
// little a child gets without asking, then what the request asks for, then PWD
// naming dir, where the child actually starts.
//
// implicitEnv is short on purpose. A child does not inherit the caller's
// environment, because a command's environment is a record of everything that
// has happened to the shell — variables a script exported, values a host passed
// in for some other command's sake — and handing all of it to a child makes the
// child's behaviour depend on that history. A request names what its script
// reads, so the script runs the same way whoever calls it.
//
// The directory variables are the sandbox's to set, not the request's to pass:
// PWD is derived from dir, so it cannot disagree with where the child is, and
// OLDPWD is left unset because the child has not been anywhere yet — a "cd -"
// with the caller's OLDPWD would jump somewhere the child has never been.
func buildChildEnv(implicitEnv, overrides []string, dir string) ([]string, error) {
	env := make([]string, 0, len(implicitEnv)+len(overrides)+1)
	env = append(env, implicitEnv...)
	for _, kv := range overrides {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || name == "" {
			return nil, fmt.Errorf("%q is not a NAME=value pair", kv)
		}
		if name == "PWD" || name == "OLDPWD" {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "PWD="+dir), nil
}
