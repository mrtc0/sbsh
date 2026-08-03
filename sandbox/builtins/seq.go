package builtins

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// seq prints a sequence of numbers. The step defaults to 1 and may be negative.
//
//	seq LAST
//	seq FIRST LAST
//	seq FIRST STEP LAST
//	-s STRING separator between numbers (default newline) / -w pad with leading zeros
func seq(_ context.Context, env *Env, args []string) error {
	fs := NewFlagSet().AllowNegativeOperands()
	sepFlag := fs.String("\n", "-s", "--separator")
	equalWidthFlag := fs.Bool("-w", "--equal-width")
	nums, err := fs.Parse(args)
	if err != nil {
		return err
	}
	sep := *sepFlag
	equalWidth := *equalWidthFlag

	if len(nums) == 0 || len(nums) > 3 {
		return fmt.Errorf("usage: seq [-w] [-s sep] [first [step]] last")
	}

	first, step, last := 1.0, 1.0, 0.0
	prec := 0
	parse := func(s string) (float64, error) {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid floating point argument: %q", s)
		}
		if p := decimalPlaces(s); p > prec {
			prec = p
		}
		return v, nil
	}

	switch len(nums) {
	case 1:
		if last, err = parse(nums[0]); err != nil {
			return err
		}
	case 2:
		if first, err = parse(nums[0]); err != nil {
			return err
		}
		if last, err = parse(nums[1]); err != nil {
			return err
		}
	case 3:
		if first, err = parse(nums[0]); err != nil {
			return err
		}
		if step, err = parse(nums[1]); err != nil {
			return err
		}
		if last, err = parse(nums[2]); err != nil {
			return err
		}
	}

	if step == 0 {
		return fmt.Errorf("step must not be zero")
	}

	values := seqValues(first, step, last)
	formatted := make([]string, len(values))
	width := 0
	for i, v := range values {
		formatted[i] = strconv.FormatFloat(v, 'f', prec, 64)
		if len(formatted[i]) > width {
			width = len(formatted[i])
		}
	}
	for i, s := range formatted {
		if equalWidth {
			s = padZero(s, width)
		}
		if i > 0 {
			fmt.Fprint(env.Stdout, sep)
		}
		fmt.Fprint(env.Stdout, s)
	}
	if len(formatted) > 0 {
		fmt.Fprintln(env.Stdout)
	}
	return nil
}

func seqValues(first, step, last float64) []float64 {
	var out []float64
	if step > 0 {
		for v := first; v <= last+1e-9; v += step {
			out = append(out, v)
		}
	} else {
		for v := first; v >= last-1e-9; v += step {
			out = append(out, v)
		}
	}
	return out
}

// decimalPlaces returns the number of digits after the decimal point in s.
func decimalPlaces(s string) int {
	if i := strings.IndexByte(s, '.'); i >= 0 {
		return len(s) - i - 1
	}
	return 0
}

// padZero left-pads s with zeros to width, keeping a leading sign in front.
func padZero(s string, width int) string {
	if len(s) >= width {
		return s
	}
	pad := strings.Repeat("0", width-len(s))
	if len(s) > 0 && (s[0] == '-' || s[0] == '+') {
		return string(s[0]) + pad + s[1:]
	}
	return pad + s
}

func init() {
	Register("seq", seq)
}
