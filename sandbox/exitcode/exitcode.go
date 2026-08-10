package exitcode

// Codes reported when a script or an interpreter is stopped by the sandbox
// rather than allowed to finish. They follow the shell convention of 128 +
// signal number, so that a caller can tell "stopped" from "failed on its own"
// the way it would for a real process.
//
// The constants are untyped: exit codes are an int in one result type and a
// uint32 in the other.
const (
	// Canceled corresponds to a cancelled context (SIGINT / Ctrl-C).
	Canceled = 128 + 2
	// Timeout corresponds to a context deadline being exceeded (SIGKILL).
	Timeout = 128 + 9
)

// Codes reported for an execution the sandbox did not run as it was asked to.
// Each is the status the execution result contract fixes for one outcome; see
// [github.com/mrtc0/sbsh/sandbox/exec.Outcome].
const (
	// Invalid is a request the sandbox cannot make sense of, following the
	// shell's status for a syntax error.
	Invalid = 2
	// Internal is a failure of the sandbox itself. No shell reports it, which is
	// the point: it cannot be mistaken for a status a request chose.
	Internal = 125
	// Denied is a request the sandbox refused to run, following the shell's
	// "cannot execute".
	Denied = 126
	// Unsupported is a request the sandbox has no way to carry out. A shell has
	// no status of its own for that, and "cannot execute" is what it amounts to,
	// so it shares Denied's status; the outcome is where the two are told apart.
	Unsupported = 126
	// NotFound is a command name that did not resolve, following the shell's
	// "command not found".
	NotFound = 127
)
