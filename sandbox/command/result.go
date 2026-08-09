package command

// ExecutionResult is what running a script in the sandbox produced.
//
// One shape covers every execution. A host calling the sandbox from outside and
// a command starting a child execution from within read the same fields with the
// same meanings, so that the answer to "how did that go" does not depend on
// which side of the sandbox boundary the question came from.
//
// ExitCode alone is not that answer. A shell has one channel for it, and it is
// too narrow: a script that exits 127 on purpose and one that named a command
// the sandbox does not have are the same status, a script the sandbox refused
// never reached a status of its own, and neither did one stopped by a deadline.
// So each reason the runtime can distinguish is its own field, and ExitCode is
// left to mean what it means in a shell.
//
// Where the two callers differ is not in this type but in what accompanies it. A
// host gets a Go error alongside a refused script or a runtime failure, because
// a host can act on one; a command starting a child gets the same fields with a
// nil error, because it reports to a shell and has no error channel to the host.
// A request that never became an execution at all — an unparseable script, a
// working directory that is not one — is an error with no result either way.
type ExecutionResult struct {
	// Stdout and Stderr are what the execution wrote, captured up to the output
	// limit.
	Stdout string
	Stderr string

	// ExitCode is the script's exit status, reduced the way a shell reports one.
	// An execution stopped by the sandbox gets the conventional 128+signal
	// status, and one it refused gets 126: see
	// [github.com/mrtc0/sbsh/sandbox/exitcode].
	ExitCode int

	// Truncated reports that the output limit cut off stdout or stderr.
	Truncated bool

	// CommandNotFound reports that a command name in the script did not resolve
	// to a builtin or a registered command. It is recorded where that is known,
	// at the dispatcher, rather than inferred from the 127 it produces — a
	// script may exit 127 perfectly deliberately.
	CommandNotFound bool

	// Denied reports that the sandbox refused the script before running it, so
	// that none of it took effect; DenialReason says why, and the same text is
	// on Stderr. Today the sandbox refuses a script for its syntax — process
	// substitution, which would reach for host resources.
	//
	// It is deliberately narrow. A denial the script runs into part-way — a path
	// the deny patterns cover, a destination outside the network policy — is not
	// reported here: it reaches the command that hit it as an ordinary error,
	// and comes back as that command's exit status and stderr, by which point
	// the rest of the script has run. Widening Denied to cover those means
	// carrying the refusal out of the filesystem and the dialer, which neither
	// is currently able to report, and it is not something to fake by matching
	// on error text. Until then, Denied means "nothing ran", and a caller that
	// needs to know a command was refused mid-script has to read its output.
	Denied       bool
	DenialReason string

	// TimedOut and Canceled report that the execution was stopped rather than
	// allowed to finish — because a deadline passed, or because the context was
	// cancelled.
	TimedOut bool
	Canceled bool

	// InternalError is set when the runtime itself failed part-way through,
	// which is neither a script failure nor a limit anybody asked for. It is
	// empty in every ordinary outcome.
	InternalError string

	// ExecutionID identifies this execution within the sandbox,
	// ParentExecutionID names the execution it hangs off, and Depth is how far
	// from the top-level script it ran. A script a host started is a root: it
	// has an id, no parent, and depth 0. They are what makes a tree of nested
	// executions readable in a log or a test.
	ExecutionID       string
	ParentExecutionID string
	Depth             int
}
