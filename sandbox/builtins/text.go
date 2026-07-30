package builtins

import (
	"strings"
)

// splitLines splits content into lines. A trailing newline is not counted as a line.
func splitLines(b []byte) []string {
	s := string(b)
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}
