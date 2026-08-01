package builtins

import (
	"context"
	"fmt"
)

// head prints the first n lines (default 10) of each input. A negative n prints
// all but the last |n| lines, as GNU head does. With no arguments it reads
// stdin. For multiple files it prefixes each with a "==> name <==" header.
func head(_ context.Context, inv *Invocation) error {
	n, files, err := parseLineCount(inv.Args, 10)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		files = []string{"-"}
	}
	for idx, f := range files {
		b, err := readSource(inv, f)
		if err != nil {
			return err
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

func init() {
	Register("head", head)
}
