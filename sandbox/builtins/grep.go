package builtins

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"

	"github.com/spf13/afero"

	"github.com/mrtc0/sbsh/sandbox/command"
)

// grep prints lines matching a regular expression. It exits 1 when nothing matches.
//
//	-i ignore case / -n line numbers / -v invert match / -r recurse into directories
//	-c print a match count per source instead of lines
//	-l print only the names of sources with a match
//	-E interpret the pattern as an extended regular expression (accepted for
//	   compatibility; the pattern is always parsed as RE2, which is ERE-compatible)
//	-A NUM print NUM lines of trailing context after each selected line
//	-B NUM print NUM lines of leading context before each selected line
//	grep [-cEilnrv] [-A NUM] [-B NUM] pattern [file...]
func grep(_ context.Context, inv *command.Invocation) error {
	fs := NewFlagSet()
	ignoreCase := fs.Bool("-i")
	lineNum := fs.Bool("-n")
	invertFlag := fs.Bool("-v")
	recursive := fs.Bool("-r")
	countFlag := fs.Bool("-c")
	listFlag := fs.Bool("-l")
	_ = fs.Bool("-E") // ERE-compatible already; accepted for compatibility
	// Repeatable so that every occurrence can be checked, not just the one that
	// wins: "grep -A x -A 2" is a mistake the user should hear about.
	afterFlag := fs.StringList("-A")
	beforeFlag := fs.StringList("-B")

	rest, err := fs.Parse(inv.Args)
	if err != nil {
		return command.Exitf(1, "%v", err)
	}
	// Both counts are settled before a byte is read, so a bad NUM fails the same
	// way whether or not the files exist.
	after, err := contextCount(*afterFlag)
	if err != nil {
		return command.Exitf(1, "%v", err)
	}
	before, err := contextCount(*beforeFlag)
	if err != nil {
		return command.Exitf(1, "%v", err)
	}
	withContext := len(*afterFlag) > 0 || len(*beforeFlag) > 0
	if len(rest) == 0 {
		return command.Exit(1, "usage: grep [-cEilnrv] [-A NUM] [-B NUM] pattern [file...]")
	}

	pattern := rest[0]
	files := rest[1:]

	expr := pattern
	if *ignoreCase {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return command.Exitf(1, "%v", err)
	}

	invert := *invertFlag
	withName := len(files) > 1 || *recursive
	countOnly := *countFlag
	listFiles := *listFlag

	matched := false
	// Whether any group has been printed yet, kept across sources: the separator
	// goes between groups wherever they come from, so two files each with a hit
	// are parted by it, and nothing is printed before the first group or after
	// the last. A source that produces no output leaves it untouched.
	groupPrinted := false

	emit := func(name string, i int, line string, selected bool) {
		// A selected line is joined to its prefix by ":", a context line by "-",
		// which is how the two are told apart once they are interleaved.
		sep := "-"
		if selected {
			sep = ":"
		}
		prefix := ""
		if withName {
			prefix += name + sep
		}
		if *lineNum {
			prefix += strconv.Itoa(i+1) + sep
		}
		fmt.Fprintln(inv.Stdout, prefix+line)
	}

	scan := func(name string, b []byte) {
		lines := splitLines(b)
		selected := make([]bool, len(lines))
		n := 0
		for i, line := range lines {
			if re.MatchString(line) == invert {
				continue
			}
			matched = true
			n++
			selected[i] = true
		}
		// -l and -c suppress the per-line output; they report per source below.
		if !listFiles && !countOnly {
			// last is the final line already printed for this source. Ranges are
			// per source: a group never reaches across a file boundary.
			last := -1
			for i := range lines {
				if !selected[i] {
					continue
				}
				// Clamped against the lines that are actually there before the
				// addition, so a NUM near the top of int cannot wrap around.
				start, end := 0, len(lines)-1
				if before < i {
					start = i - before
				}
				if after < len(lines)-1-i {
					end = i + after
				}
				if last >= 0 && start <= last+1 {
					// Overlapping or touching the lines just printed: the two are
					// one group, and the shared lines are not repeated.
					start = last + 1
					if end < start {
						continue
					}
				} else if withContext && groupPrinted {
					fmt.Fprintln(inv.Stdout, "--")
				}
				for j := start; j <= end; j++ {
					emit(name, j, lines[j], selected[j])
				}
				last = end
				groupPrinted = true
			}
		}
		switch {
		case listFiles:
			if n > 0 {
				fmt.Fprintln(inv.Stdout, name)
			}
		case countOnly:
			prefix := ""
			if withName {
				prefix += name + ":"
			}
			fmt.Fprintf(inv.Stdout, "%s%d\n", prefix, n)
		}
	}

	guard := &walkGuard{inv: inv}

	if len(files) == 0 {
		b, err := readSource(inv, "-")
		if err != nil {
			return command.Exitf(1, "%v", err)
		}
		scan("(standard input)", b)
	} else {
		for _, f := range files {
			abs := inv.Abs(f)
			info, err := inv.FS.Stat(abs)
			if err != nil {
				if guard.skip(err) {
					continue
				}
				return command.Exitf(1, "%v", err)
			}
			if info.IsDir() {
				if !*recursive {
					return command.Exitf(1, "%q is a directory", f)
				}
				err := afero.Walk(inv.FS, abs, guard.wrap(func(p string, fi os.FileInfo, _ error) error {
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
					b, err := afero.ReadFile(inv.FS, p)
					if err != nil {
						return err
					}
					scan(walkedName(f, abs, p), b)
					return nil
				}))
				if err != nil {
					return command.Exitf(1, "%v", err)
				}
				continue
			}
			b, err := afero.ReadFile(inv.FS, abs)
			if err != nil {
				if guard.skip(err) {
					continue
				}
				return command.Exitf(1, "%v", err)
			}
			scan(f, b)
		}
	}

	// A refusal outranks the match/no-match answer, as it does in GNU grep: 1
	// stays reserved for "nothing matched", so 2 is what reports an error.
	switch {
	case guard.refused:
		return command.Exit(2)
	case !matched:
		return command.Exit(1)
	}
	return nil
}

// contextCount reads the NUM of a context option. Every value given is checked,
// not only the last, and the last one is what takes effect; with the option
// absent the count is zero. NUM is a non-negative decimal written in ASCII
// digits and no wider than an int, so "-1", "1.5", "2x", "" and a number past
// the int range are each refused rather than quietly reinterpreted.
func contextCount(vals []string) (int, error) {
	n := 0
	for _, v := range vals {
		if !isDecimalDigits(v) {
			return 0, fmt.Errorf("%s: invalid context length argument", v)
		}
		// The digits are already known good, so the only error left for Atoi to
		// give is a value too wide for an int.
		parsed, err := strconv.Atoi(v)
		if err != nil {
			return 0, fmt.Errorf("%s: invalid context length argument", v)
		}
		n = parsed
	}
	return n, nil
}

// isDecimalDigits reports whether s is one or more ASCII decimal digits and
// nothing else: no sign, no separators, no other scripts' digits.
func isDecimalDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func init() {
	Register("grep", grep)
}
