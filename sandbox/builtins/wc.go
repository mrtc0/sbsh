package builtins

import (
	"context"
	"fmt"
	"strings"
)

// wc counts lines, words, and bytes. -l/-w/-c select what to count (all when unspecified).
// With no arguments it reads stdin. For multiple files it prints a trailing total.
func wc(_ context.Context, env *Env) error {
	fs := NewFlagSet()
	showLinesFlag := fs.Bool("-l")
	showWordsFlag := fs.Bool("-w")
	showBytesFlag := fs.Bool("-c")
	files, err := fs.Parse(env.Args)
	if err != nil {
		return err
	}
	showLines, showWords, showBytes := *showLinesFlag, *showWordsFlag, *showBytesFlag
	if !showLines && !showWords && !showBytes {
		showLines, showWords, showBytes = true, true, true
	}

	emit := func(name string, l, w, c int) {
		var b strings.Builder
		if showLines {
			fmt.Fprintf(&b, "%8d", l)
		}
		if showWords {
			fmt.Fprintf(&b, "%8d", w)
		}
		if showBytes {
			fmt.Fprintf(&b, "%8d", c)
		}
		if name != "" {
			fmt.Fprintf(&b, " %s", name)
		}
		fmt.Fprintln(env.Stdout, b.String())
	}

	count := func(b []byte) (lines, words, bytes int) {
		s := string(b)
		return strings.Count(s, "\n"), len(strings.Fields(s)), len(b)
	}

	if len(files) == 0 {
		b, err := readSource(env, "-")
		if err != nil {
			return err
		}
		l, w, c := count(b)
		emit("", l, w, c)
		return nil
	}

	var totL, totW, totC int
	for _, f := range files {
		b, err := readSource(env, f)
		if err != nil {
			return err
		}
		l, w, c := count(b)
		totL, totW, totC = totL+l, totW+w, totC+c
		emit(f, l, w, c)
	}
	if len(files) > 1 {
		emit("total", totL, totW, totC)
	}
	return nil
}

func init() {
	Register("wc", wc)
}
