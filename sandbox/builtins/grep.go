package builtins

import (
	"context"
	"fmt"
	"os"
	"regexp"

	"github.com/spf13/afero"
)

// grep prints lines matching a regular expression. It exits 1 when nothing matches.
//
//	-i ignore case / -n line numbers / -v invert match / -r recurse into directories
//	-c print a match count per source instead of lines
//	-l print only the names of sources with a match
//	-E interpret the pattern as an extended regular expression (accepted for
//	   compatibility; the pattern is always parsed as RE2, which is ERE-compatible)
//	grep [-cEilnrv] pattern [file...]
func grep(_ context.Context, env *Env, args []string) error {
	fs := NewFlagSet()
	ignoreCase := fs.Bool("-i")
	lineNum := fs.Bool("-n")
	invertFlag := fs.Bool("-v")
	recursive := fs.Bool("-r")
	countFlag := fs.Bool("-c")
	listFlag := fs.Bool("-l")
	_ = fs.Bool("-E") // ERE-compatible already; accepted for compatibility

	rest, err := fs.Parse(args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return fmt.Errorf("usage: grep [-cEilnrv] pattern [file...]")
	}

	pattern := rest[0]
	files := rest[1:]

	expr := pattern
	if *ignoreCase {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return err
	}

	invert := *invertFlag
	withName := len(files) > 1 || *recursive
	countOnly := *countFlag
	listFiles := *listFlag

	matched := false
	scan := func(name string, b []byte) {
		n := 0
		for i, line := range splitLines(b) {
			if re.MatchString(line) == invert {
				continue
			}
			matched = true
			n++
			// -l and -c suppress the per-line output; they report per source below.
			if listFiles || countOnly {
				continue
			}
			prefix := ""
			if withName {
				prefix += name + ":"
			}
			if *lineNum {
				prefix += fmt.Sprintf("%d:", i+1)
			}
			fmt.Fprintln(env.Stdout, prefix+line)
		}
		switch {
		case listFiles:
			if n > 0 {
				fmt.Fprintln(env.Stdout, name)
			}
		case countOnly:
			prefix := ""
			if withName {
				prefix += name + ":"
			}
			fmt.Fprintf(env.Stdout, "%s%d\n", prefix, n)
		}
	}

	guard := &walkGuard{env: env}

	if len(files) == 0 {
		b, err := readSource(env, "-")
		if err != nil {
			return err
		}
		scan("(standard input)", b)
	} else {
		for _, f := range files {
			abs := env.Abs(f)
			info, err := env.FS.Stat(abs)
			if err != nil {
				if guard.skip(err) {
					continue
				}
				return err
			}
			if info.IsDir() {
				if !*recursive {
					return fmt.Errorf("%q is a directory", f)
				}
				err := afero.Walk(env.FS, abs, guard.wrap(func(p string, fi os.FileInfo, _ error) error {
					if fi.IsDir() {
						return nil
					}
					// A link met while walking is skipped without a word, as it is
					// under GNU grep's -r: reading through it would report the same
					// file under a second name, and for a link the mount cannot
					// follow it would fail on an entry no argument named. A link
					// given as an argument is still read, since Stat above follows
					// it.
					if fi.Mode()&os.ModeSymlink != 0 {
						return nil
					}
					b, err := afero.ReadFile(env.FS, p)
					if err != nil {
						return err
					}
					scan(walkedName(f, abs, p), b)
					return nil
				}))
				if err != nil {
					return err
				}
				continue
			}
			b, err := afero.ReadFile(env.FS, abs)
			if err != nil {
				if guard.skip(err) {
					continue
				}
				return err
			}
			scan(f, b)
		}
	}

	// A refusal outranks the match/no-match answer, as it does in GNU grep: 1
	// stays reserved for "nothing matched", so 2 is what reports an error.
	switch {
	case guard.refused:
		return exit(2)
	case !matched:
		return exit(1)
	}
	return nil
}

func init() {
	Register("grep", grep)
}
