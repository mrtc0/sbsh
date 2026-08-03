package builtins

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// cut selects portions of each input line. Exactly one of -f or -c must be
// given. With no files it reads stdin; multiple files are processed in order.
//
//	-f LIST select fields, split on -d (default TAB)
//	-c LIST select characters by position
//	-d DELIM field delimiter for -f
//	-s with -f, skip lines that contain no delimiter
//	cut -f LIST [-d DELIM] [-s] [file...]
//	cut -c LIST [file...]
func cut(_ context.Context, env *Env, args []string) error {
	fs := NewFlagSet()
	fieldsSpec := fs.String("", "-f")
	charsSpec := fs.String("", "-c")
	delimFlag := fs.String("\t", "-d")
	onlyDelim := fs.Bool("-s", "--only-delimited")
	files, err := fs.Parse(args)
	if err != nil {
		return err
	}
	haveFields, haveChars := fs.Seen("-f"), fs.Seen("-c")
	delim := *delimFlag
	onlyDelimited := *onlyDelim

	if haveFields == haveChars {
		return fmt.Errorf("usage: cut (-f LIST [-d DELIM] [-s] | -c LIST) [file...]")
	}

	spec := *fieldsSpec
	if haveChars {
		spec = *charsSpec
	}
	ranges, err := parseCutRanges(spec)
	if err != nil {
		return err
	}

	if len(files) == 0 {
		files = []string{"-"}
	}
	for _, f := range files {
		b, err := readSource(env, f)
		if err != nil {
			return err
		}
		for _, line := range splitLines(b) {
			if haveChars {
				fmt.Fprintln(env.Stdout, selectChars(line, ranges))
				continue
			}
			if !strings.Contains(line, delim) {
				if !onlyDelimited {
					fmt.Fprintln(env.Stdout, line)
				}
				continue
			}
			fmt.Fprintln(env.Stdout, selectFields(line, delim, ranges))
		}
	}
	return nil
}

// cutRange is an inclusive 1-based range. A lo/hi of 0 means open-ended on that
// side (e.g. "-3" is lo=0,hi=3; "3-" is lo=3,hi=0).
type cutRange struct{ lo, hi int }

func parseCutRanges(spec string) ([]cutRange, error) {
	if spec == "" {
		return nil, fmt.Errorf("empty list")
	}
	var ranges []cutRange
	for _, part := range strings.Split(spec, ",") {
		if part == "" {
			continue
		}
		if i := strings.IndexByte(part, '-'); i >= 0 {
			lo, hi := 0, 0
			var err error
			if i > 0 {
				if lo, err = strconv.Atoi(part[:i]); err != nil {
					return nil, fmt.Errorf("invalid range: %q", part)
				}
			}
			if i < len(part)-1 {
				if hi, err = strconv.Atoi(part[i+1:]); err != nil {
					return nil, fmt.Errorf("invalid range: %q", part)
				}
			}
			ranges = append(ranges, cutRange{lo, hi})
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("invalid field: %q", part)
		}
		ranges = append(ranges, cutRange{n, n})
	}
	return ranges, nil
}

func inRanges(pos int, ranges []cutRange) bool {
	for _, r := range ranges {
		if (r.lo == 0 || pos >= r.lo) && (r.hi == 0 || pos <= r.hi) {
			return true
		}
	}
	return false
}

func selectChars(line string, ranges []cutRange) string {
	runes := []rune(line)
	var b strings.Builder
	for i, r := range runes {
		if inRanges(i+1, ranges) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func selectFields(line, delim string, ranges []cutRange) string {
	fields := strings.Split(line, delim)
	var out []string
	for i, f := range fields {
		if inRanges(i+1, ranges) {
			out = append(out, f)
		}
	}
	return strings.Join(out, delim)
}

func init() {
	Register("cut", cut)
}
