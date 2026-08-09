package sandbox

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/mrtc0/sh/v3/interp"
	"github.com/mrtc0/sh/v3/syntax"

	"github.com/mrtc0/sbsh/sandbox/command"
	"github.com/mrtc0/sbsh/vfs"
)

// nestedExecutor is what a running command is handed as [command.NestedExecutor].
//
// It is built once per invocation, when the dispatcher already knows where the
// command is running and what it can see, so a command that never nests pays
// only for the struct.
type nestedExecutor struct {
	sandbox *Sandbox

	// parent is the execution the command is part of, and the one a child hangs
	// off.
	parent *execution

	// dir and env are the calling command's view at the moment it was invoked,
	// not the execution's starting one: a script that has since changed
	// directory or exported a variable passes that on to the children it starts.
	//
	// env holds the exported variables only. A child is a fresh shell, so what it
	// inherits is what a child shell inherits.
	dir string
	env []string
}

// nestedExecutorFor builds the executor for one invocation. It returns nil when
// the context carries no execution, leaving [command.Invocation.Nested] nil —
// the honest answer for an invocation the sandbox did not start, such as one a
// test builds by hand.
func (s *Sandbox) nestedExecutorFor(ctx context.Context, inv *command.Invocation) command.NestedExecutor {
	parent := executionFrom(ctx)
	if parent == nil {
		return nil
	}
	// Exported variables only, and none at all from an environment that cannot
	// say which those are. See [command.ExportedEnviron].
	var env []string
	if exported, ok := inv.Env.(command.ExportedEnviron); ok {
		env = exported.Exported()
	}
	return &nestedExecutor{sandbox: s, parent: parent, dir: inv.Dir, env: env}
}

// Run evaluates the request as a child of the execution the calling command
// belongs to.
//
// It is deliberately not a call back into [Sandbox.Exec]. Exec is the sandbox's
// front door and holds s.mu for the whole run, so re-entering it from inside a
// command — which runs while that lock is held — would deadlock the sandbox
// against itself. The two concerns are separate: s.mu is admission control for
// top-level runs, and a child needs none of it, being already inside the one run
// that was admitted.
//
// The child gets an interpreter of its own rather than the sandbox's shared one.
// That is what makes the re-entrancy safe — the parent is in the middle of using
// the shared runner — and it is also the state boundary the model promises: the
// child inherits the parent's directory and variables as a starting point, and
// whatever it does to them stays in the child.
func (e *nestedExecutor) Run(ctx context.Context, req command.NestedRunRequest) (*command.ExecutionResult, error) {
	s := e.sandbox

	if strings.TrimSpace(req.Script) == "" {
		return nil, errors.New("sandbox: nested execution: script is empty")
	}
	if e.parent.depth+1 > command.MaxNestingDepth {
		return nil, fmt.Errorf("sandbox: %w (%d)", command.ErrMaxDepth, command.MaxNestingDepth)
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(req.Script), "nested")
	if err != nil {
		return nil, fmt.Errorf("sandbox: nested execution: parse: %w", err)
	}
	dir, err := e.childDir(req.Dir)
	if err != nil {
		return nil, err
	}

	child := s.newChildExecution(e.parent, req.OutputLimit)
	res := &command.ExecutionResult{
		ExecutionID:       child.id,
		ParentExecutionID: child.parentID,
		Depth:             child.depth,
	}

	// A script the sandbox refuses is a denial, not a failure of the script: it
	// never ran. The same helper the top level uses fills it in, and unlike the
	// top level there is no error beside it — a command has nowhere to put one.
	if err := quarantine(file); err != nil {
		denied(res, err)
		return res, nil
	}

	// context.WithTimeout is what clamps the child's deadline: it keeps whichever
	// of the two comes first, so a request can only bring the parent's budget
	// closer, never push it out.
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	ctx = withExecution(ctx, child)
	ctx, unresolved := command.WithUnresolvedRecorder(ctx)

	stdout := newCapWriter(child.outputLimit)
	stderr := newCapWriter(child.outputLimit)

	runner, err := s.newRunner(dir, e.childEnv(req.Env, dir), interp.StdIO(strings.NewReader(""), stdout, stderr))
	if err != nil {
		return nil, fmt.Errorf("sandbox: nested execution: creating interpreter: %w", err)
	}

	runErr := runner.Run(ctx, file)

	res.Stdout = stdout.String()
	res.Stderr = stderr.String()
	res.Truncated = stdout.Truncated() || stderr.Truncated()
	_, res.CommandNotFound = unresolved()
	normalizeOutcome(ctx, res, runErr)
	return res, nil
}

// childDir picks the directory the child starts in: the requested one when there
// is one, and the calling command's otherwise. A requested directory has to
// exist in the sandbox filesystem and be a directory — a child that cannot start
// is a bad request, not a failed script.
func (e *nestedExecutor) childDir(requested string) (string, error) {
	parent := e.dir
	if parent == "" {
		parent = "/"
	}
	if requested == "" {
		return parent, nil
	}

	dir := requested
	if !path.IsAbs(dir) {
		dir = path.Join(parent, dir)
	}
	dir = vfs.Normalize(dir)
	info, err := e.sandbox.fs.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("sandbox: nested execution: dir %q: %w", requested, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("sandbox: nested execution: dir %q: not a directory", requested)
	}
	return dir, nil
}

// childEnv layers the request's entries over the variables the calling command
// can see, and pins PWD to where the child actually starts so that the two
// cannot disagree.
func (e *nestedExecutor) childEnv(overrides []string, dir string) []string {
	env := make([]string, 0, len(e.env)+len(overrides)+1)
	env = append(env, e.env...)
	env = append(env, overrides...)
	return append(env, "PWD="+dir)
}
