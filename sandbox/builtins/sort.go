package builtins

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/mrtc0/sbsh/sandbox/command"
)

// sortCommand orders input lines. With no files it reads stdin; multiple files
// are concatenated before sorting.
//
//	-r reverse / -n numeric / -u drop duplicate lines / -f fold case
//	sort [-rnuf] [file...]
func sortCommand(_ context.Context, inv *command.Invocation) error {
	fs := NewFlagSet()
	reverseFlag := fs.Bool("-r")
	numericFlag := fs.Bool("-n")
	uniqueFlag := fs.Bool("-u")
	foldFlag := fs.Bool("-f")
	files, err := fs.Parse(inv.Args)
	if err != nil {
		return command.Exitf(1, "%v", err)
	}
	reverse, numeric, unique, fold := *reverseFlag, *numericFlag, *uniqueFlag, *foldFlag

	var lines []string
	if len(files) == 0 {
		b, err := readSource(inv, "-")
		if err != nil {
			return command.Exitf(1, "%v", err)
		}
		lines = splitLines(b)
	} else {
		for _, f := range files {
			b, err := readSource(inv, f)
			if err != nil {
				return command.Exitf(1, "%v", err)
			}
			lines = append(lines, splitLines(b)...)
		}
	}

	key := func(s string) string {
		if fold {
			return strings.ToLower(s)
		}
		return s
	}
	less := func(i, j int) bool {
		a, b := lines[i], lines[j]
		if numeric {
			na, nb := parseLeadingNum(a), parseLeadingNum(b)
			if na != nb {
				return na < nb
			}
		}
		return key(a) < key(b)
	}
	sort.SliceStable(lines, func(i, j int) bool {
		if reverse {
			return less(j, i)
		}
		return less(i, j)
	})

	if unique {
		out := lines[:0]
		var prevKey string
		have := false
		for _, l := range lines {
			k := key(l)
			if have && k == prevKey {
				continue
			}
			out = append(out, l)
			prevKey, have = k, true
		}
		lines = out
	}

	for _, l := range lines {
		fmt.Fprintln(inv.Stdout, l)
	}
	return nil
}

// parseLeadingNum reads the numeric prefix of s for -n comparisons. Lines
// without a leading number sort as 0, matching GNU sort's default behavior.
func parseLeadingNum(s string) float64 {
	s = strings.TrimSpace(s)
	i := 0
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		i++
	}
	dot := false
	for i < len(s) {
		c := s[i]
		if c >= '0' && c <= '9' {
			i++
			continue
		}
		if c == '.' && !dot {
			dot = true
			i++
			continue
		}
		break
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s[:i]), 64)
	if err != nil {
		return 0
	}
	return v
}

func init() {
	Register("sort", sortCommand)
}
