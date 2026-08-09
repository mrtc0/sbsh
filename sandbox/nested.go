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
	"github.com/mrtc0/sbsh/sandbox/exitcode"
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
func (e *nestedExecutor) Run(ctx context.Context, req command.NestedRunRequest) (*command.NestedRunResult, error) {
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
	res := &command.NestedRunResult{
		ExecutionID:       child.id,
		ParentExecutionID: child.parentID,
		Depth:             child.depth,
	}

	// A script the sandbox refuses is a denial, not a failure of the script: it
	// never ran. Reporting it in the result rather than as an error keeps it in
	// the same shape as every other outcome the caller has to branch on.
	if err := quarantine(file); err != nil {
		res.Denied = true
		res.DenialReason = err.Error()
		res.ExitCode = exitcode.Denied
		res.Stderr = "sbsh: " + err.Error() + "\n"
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
	normalizeNested(ctx, res, runErr)
	return res, nil
}

// normalizeNested turns whatever the interpreter returned into the result's
// fields, so that a caller reads one shape whether the script exited on its own,
// was stopped, or the runtime came apart under it.
//
// A stop is decided before an exit status is read, and on the context rather
// than on what came back. Being stopped is the fact a caller most needs, and it
// is the one the shell backend is least obliged to preserve: it may report a
// deadline as its own error today and as a wrapped exit status tomorrow, and
// either way TimedOut and Canceled have to stay true. The cost is that a script
// exiting at the very moment its deadline passes is reported as stopped rather
// than by the status it happened to reach, which is the reading worth keeping.
func normalizeNested(ctx context.Context, res *command.NestedRunResult, runErr error) {
	// A run that finished cleanly is not reconsidered: a context that expires
	// just after the last command would otherwise turn a success into a timeout.
	if runErr == nil {
		return
	}

	// A deadline or a cancellation is a limit somebody asked for, not a
	// malfunction, so it gets the conventional 128+signal status the same way a
	// top-level run does.
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.TimedOut = true
		res.ExitCode = exitcode.Timeout
		return
	case errors.Is(ctx.Err(), context.Canceled):
		res.Canceled = true
		res.ExitCode = exitcode.Canceled
		return
	}

	var exitStatus interp.ExitStatus
	if errors.As(runErr, &exitStatus) {
		res.ExitCode = int(exitStatus)
		return
	}

	res.InternalError = runErr.Error()
	res.ExitCode = 1
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
