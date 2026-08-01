package builtins

import (
	"context"
	"fmt"
)

// tail prints the last n lines (default 10) of each input. A negative n is
// treated as its magnitude, so "-n -K" also prints the last K lines. With no
// arguments it reads stdin. For multiple files it prefixes each with a
// "==> name <==" header.
func tail(_ context.Context, inv *Invocation) error {
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
		count := n
		if count < 0 {
			count = -count
		}
		start := len(lines) - count
		if start < 0 {
			start = 0
		}
		for _, l := range lines[start:] {
			fmt.Fprintln(inv.Stdout, l)
		}
	}
	return nil
}

func init() {
	Register("tail", tail)
}
