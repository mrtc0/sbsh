package exitcode

// Codes reported when a script or an interpreter is stopped by the sandbox
// rather than allowed to finish. They follow the shell's own conventions, so
// that a caller can tell "stopped" from "failed on its own" the way it would for
// a real process.
//
// The constants are untyped: exit codes are an int in one result type and a
// uint32 in the other.
const (
	// Canceled corresponds to a cancelled context (SIGINT / Ctrl-C).
	Canceled = 128 + 2
	// Timeout corresponds to a context deadline being exceeded (SIGKILL).
	Timeout = 128 + 9
	// Denied is reported when the sandbox refuses to run something at all, so
	// that it never reached a status of its own. It follows the shell's 126,
	// which says a command was found but could not be run.
	Denied = 126
)
