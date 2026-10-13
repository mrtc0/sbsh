package builtins

import (
	"context"
	"fmt"

	"github.com/mrtc0/sbsh/sandbox/command"
)

// head prints the first n lines (default 10) of each input. A negative n prints
// all but the last |n| lines, as GNU head does. With no arguments it reads
// stdin. For multiple files it prefixes each with a "==> name <==" header. A
// leading "-NUM" argument is shorthand for "-n NUM".
func head(_ context.Context, inv *command.Invocation) error {
	n, files, err := parseLineCount(expandLeadingLineCount(inv.Args), 10)
	if err != nil {
		return command.Exitf(1, "%v", err)
	}
	if len(files) == 0 {
		files = []string{"-"}
	}
	for idx, f := range files {
		b, err := readSource(inv, f)
		if err != nil {
			return command.Exitf(1, "%v", err)
		}
		if len(files) > 1 {
			if idx > 0 {
				fmt.Fprintln(inv.Stdout)
			}
			fmt.Fprintf(inv.Stdout, "==> %s <==\n", f)
		}
		lines := splitLines(b)
		limit := n
		if limit < 0 {
			// -n -K: keep all but the last |n| lines.
			limit = len(lines) + n
		}
		if limit < 0 {
			limit = 0
		}
		if limit > len(lines) {
			limit = len(lines)
		}
		for _, l := range lines[:limit] {
			fmt.Fprintln(inv.Stdout, l)
		}
	}
	return nil
}

// expandLeadingLineCount rewrites a first argument of the form -NUM (ASCII
// digits only) into "-n", "NUM". Any other args are returned unchanged. It
// returns a new slice and never modifies args or its backing array. NUM is left
// for parseLineCount to convert, so a later -n still takes precedence.
func expandLeadingLineCount(args []string) []string {
	if len(args) == 0 || !isLeadingLineCount(args[0]) {
		return args
	}
	out := make([]string, 0, len(args)+1)
	out = append(out, "-n", args[0][1:])
	return append(out, args[1:]...)
}

// isLeadingLineCount reports whether s is "-" followed by one or more ASCII digits.
func isLeadingLineCount(s string) bool {
	return len(s) >= 2 && s[0] == '-' && isDecimalDigits(s[1:])
}

func init() {
	Register("head", head)
}
