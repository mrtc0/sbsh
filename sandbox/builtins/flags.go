package builtins

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/afero"

	"github.com/mrtc0/sbsh/sandbox/command"
)

// FlagSet is the one option parser every builtin uses. A command declares its
// flags with Bool, String, and StringList (each returns a pointer that Parse
// fills in), then calls Parse to get the remaining operands.
//
// It follows a consistent GNU/getopt subset:
//
//   - short flags may be grouped, e.g. -rn is -r -n;
//   - a value may be attached (-nN, -Fvalue) or given as the next argument
//     (-n N), and the next argument is taken verbatim even if it looks like a
//     flag, so -n -5 sets the value to "-5";
//   - long flags accept --name, --name=value, or --name value;
//   - "--" ends option parsing and "-" is always an operand (the stdin
//     sentinel), never a flag;
//   - by default options and operands may be interleaved (GNU permutation), so
//     `sort file -r` still sees -r. StopAtFirstOperand turns that off for
//     commands like awk whose first operand is a program that must not be
//     scanned for flags.
//
// It deliberately does not parse numbers or other domain types: a command reads
// the raw string from its String flag and converts it with its own error
// message. AllowNegativeOperands lets a command like seq accept negative
// numeric operands (-3) instead of rejecting them as unknown flags.
type FlagSet struct {
	lookup      map[string]*flagDef
	negOperands bool
	stopAtFirst bool
}

type flagKind int

const (
	kindBool flagKind = iota
	kindString
	kindStringList
)

type flagDef struct {
	kind    flagKind
	boolVal *bool
	strVal  *string
	listVal *[]string
	// canonical is the first registered name, used in error messages and by Seen.
	canonical string
	seen      bool
}

func NewFlagSet() *FlagSet {
	return &FlagSet{lookup: map[string]*flagDef{}}
}

// AllowNegativeOperands treats tokens like "-3" or "-.5" as operands rather than
// unknown flags. Use it for commands whose operands can be negative numbers.
func (f *FlagSet) AllowNegativeOperands() *FlagSet {
	f.negOperands = true
	return f
}

// StopAtFirstOperand stops option parsing at the first operand, so every token
// after it is an operand even if it begins with a dash.
func (f *FlagSet) StopAtFirstOperand() *FlagSet {
	f.stopAtFirst = true
	return f
}

// Seen reports whether the flag with the given name appeared in the arguments.
// It lets a command distinguish an explicit value from a default.
func (f *FlagSet) Seen(name string) bool {
	d, ok := f.lookup[name]
	return ok && d.seen
}

func (f *FlagSet) register(d *flagDef, names []string) {
	if len(names) == 0 {
		panic("builtins: flag registered without a name")
	}
	d.canonical = names[0]
	for _, n := range names {
		f.lookup[n] = d
	}
}

// Bool registers a boolean flag under the given names (e.g. "-r", "-R").
func (f *FlagSet) Bool(names ...string) *bool {
	v := new(bool)
	f.register(&flagDef{kind: kindBool, boolVal: v}, names)
	return v
}

// String registers a value flag with a default. If the flag appears more than
// once, the last value wins.
func (f *FlagSet) String(def string, names ...string) *string {
	v := new(string)
	*v = def
	f.register(&flagDef{kind: kindString, strVal: v}, names)
	return v
}

// StringList registers a repeatable value flag; each occurrence appends.
func (f *FlagSet) StringList(names ...string) *[]string {
	v := new([]string)
	f.register(&flagDef{kind: kindStringList, listVal: v}, names)
	return v
}

// Parse consumes args and returns the operands in order.
func (f *FlagSet) Parse(args []string) ([]string, error) {
	var operands []string
	i := 0
	for i < len(args) {
		t := args[i]
		switch {
		case t == "--":
			operands = append(operands, args[i+1:]...)
			return operands, nil
		case t == "-" || !strings.HasPrefix(t, "-"):
			return f.appendOperand(operands, args, i)
		case f.negOperands && isNumericToken(t):
			return f.appendOperand(operands, args, i)
		case strings.HasPrefix(t, "--"):
			next, err := f.parseLong(t, args, i)
			if err != nil {
				return nil, err
			}
			i = next
		default:
			next, err := f.parseShort(t, args, i)
			if err != nil {
				return nil, err
			}
			i = next
		}
	}
	return operands, nil
}

// appendOperand records the operand at index i. Under GNU permutation it keeps
// scanning the rest; StopAtFirstOperand takes everything from here as operands.
func (f *FlagSet) appendOperand(operands, args []string, i int) ([]string, error) {
	if f.stopAtFirst {
		return append(operands, args[i:]...), nil
	}
	operands = append(operands, args[i])
	rest, err := f.Parse(args[i+1:])
	if err != nil {
		return nil, err
	}
	return append(operands, rest...), nil
}

func (f *FlagSet) parseShort(t string, args []string, i int) (int, error) {
	body := t[1:]
	for j := 0; j < len(body); j++ {
		name := "-" + string(body[j])
		d, ok := f.lookup[name]
		if !ok {
			return 0, fmt.Errorf("invalid option -- %c", body[j])
		}
		if d.kind == kindBool {
			*d.boolVal = true
			d.seen = true
			continue
		}
		// Value flag: the value is the rest of the group, or the next argument.
		if rest := body[j+1:]; rest != "" {
			f.setValue(d, rest)
			return i + 1, nil
		}
		if i+1 >= len(args) {
			return 0, fmt.Errorf("option requires an argument -- %c", body[j])
		}
		f.setValue(d, args[i+1])
		return i + 2, nil
	}
	return i + 1, nil
}

func (f *FlagSet) parseLong(t string, args []string, i int) (int, error) {
	body := t[2:]
	name, val, hasVal := body, "", false
	if eq := strings.IndexByte(body, '='); eq >= 0 {
		name, val, hasVal = body[:eq], body[eq+1:], true
	}
	d, ok := f.lookup["--"+name]
	if !ok {
		return 0, fmt.Errorf("unrecognized option '--%s'", name)
	}
	if d.kind == kindBool {
		if hasVal {
			return 0, fmt.Errorf("option '--%s' doesn't allow an argument", name)
		}
		*d.boolVal = true
		d.seen = true
		return i + 1, nil
	}
	if hasVal {
		f.setValue(d, val)
		return i + 1, nil
	}
	if i+1 >= len(args) {
		return 0, fmt.Errorf("option '--%s' requires an argument", name)
	}
	f.setValue(d, args[i+1])
	return i + 2, nil
}

func (f *FlagSet) setValue(d *flagDef, v string) {
	d.seen = true
	switch d.kind {
	case kindString:
		*d.strVal = v
	case kindStringList:
		*d.listVal = append(*d.listVal, v)
	}
}

// isNumericToken reports whether t is a dash followed by a digit or dot, i.e. a
// negative number rather than an option.
func isNumericToken(t string) bool {
	if len(t) < 2 || t[0] != '-' {
		return false
	}
	c := t[1]
	return (c >= '0' && c <= '9') || c == '.'
}

// parseLineCount parses head/tail's "-n" option through the shared FlagSet and
// returns the line count and the operand files. It uses def when -n is absent.
// A dash-prefixed value is taken verbatim, so "-n -5" yields a count of -5.
func parseLineCount(args []string, def int) (n int, files []string, err error) {
	fs := NewFlagSet()
	nFlag := fs.String(strconv.Itoa(def), "-n")
	files, err = fs.Parse(args)
	if err != nil {
		return 0, nil, err
	}
	n, err = strconv.Atoi(*nFlag)
	if err != nil {
		return 0, nil, fmt.Errorf("invalid line count: %q", *nFlag)
	}
	return n, files, nil
}

// readSource returns the file contents at path. When path is "" or "-" it reads from
// stdin (empty if Stdin is nil).
func readSource(inv *command.Invocation, path string) ([]byte, error) {
	if path == "" || path == "-" {
		if inv.Stdin == nil {
			return nil, nil
		}
		return io.ReadAll(inv.Stdin)
	}
	return afero.ReadFile(inv.FS, inv.Abs(path))
}
