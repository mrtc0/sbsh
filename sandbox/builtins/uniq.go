package builtins

import (
	"context"
	"fmt"
	"strings"
)

// uniqCommand filters adjacent duplicate lines. Like the real uniq it only
// collapses runs that are already adjacent, so input is usually piped through
// sort first.
//
//	-c prefix counts / -d only duplicated lines / -u only unique lines / -i ignore case
//	uniq [-cdui] [file]
func uniqCommand(_ context.Context, env *Env, args []string) error {
	fs := NewFlagSet()
	count := fs.Bool("-c")
	onlyDup := fs.Bool("-d")
	onlyUniq := fs.Bool("-u")
	fold := fs.Bool("-i")
	files, err := fs.Parse(args)
	if err != nil {
		return err
	}
	if len(files) > 1 {
		return fmt.Errorf("usage: uniq [-cdui] [file]")
	}

	src := "-"
	if len(files) == 1 {
		src = files[0]
	}
	b, err := readSource(env, src)
	if err != nil {
		return err
	}
	lines := splitLines(b)

	key := func(s string) string {
		if *fold {
			return strings.ToLower(s)
		}
		return s
	}

	for i := 0; i < len(lines); {
		j := i + 1
		for j < len(lines) && key(lines[j]) == key(lines[i]) {
			j++
		}
		n := j - i
		switch {
		case *onlyDup && n < 2:
		case *onlyUniq && n > 1:
		case *count:
			fmt.Fprintf(env.HC.Stdout, "%7d %s\n", n, lines[i])
		default:
			fmt.Fprintln(env.HC.Stdout, lines[i])
		}
		i = j
	}
	return nil
}

func init() {
	Register("uniq", uniqCommand)
}
