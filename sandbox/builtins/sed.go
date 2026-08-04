package builtins

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/afero"

	"github.com/mrtc0/sbsh/sandbox/command"
)

// sedCommand is a stream editor covering the subset a coding agent reaches for:
// substitution, line deletion, and explicit printing, optionally restricted by
// an address.
//
//	sed [-n] [-i] [-e script]... [script] [file...]
//
// Commands (optionally prefixed by an address N, N,M, or /regexp/):
//
//	s/regexp/replacement/[g][i][p]   substitute
//	d                                delete the line
//	p                                print the line
//
// Regular expressions use Go's regexp (RE2) syntax. Replacements understand &
// (whole match), \1..\9 (capture groups), \n and \t. -i edits files in place;
// otherwise the result is written to stdout. The "No newline at end of file"
// convention is not emitted, but a missing trailing newline is preserved.
func sedCommand(_ context.Context, inv *command.Invocation) error {
	// The script (when no -e is given) is the first operand, so stop scanning
	// options there and treat the rest as the script and files.
	fs := NewFlagSet().StopAtFirstOperand()
	quietFlag := fs.Bool("-n")
	inPlaceFlag := fs.Bool("-i")
	exprFlags := fs.StringList("-e")
	rest, err := fs.Parse(inv.Args)
	if err != nil {
		return command.Exitf(1, "%v", err)
	}
	quiet := *quietFlag
	inPlace := *inPlaceFlag
	exprs := *exprFlags
	if len(exprs) == 0 {
		if len(rest) == 0 {
			return command.Exit(1, "usage: sed [-n] [-i] [-e script] [script] [file...]")
		}
		exprs = append(exprs, rest[0])
		rest = rest[1:]
	}

	cmds, err := parseSedScript(strings.Join(exprs, "\n"))
	if err != nil {
		return command.Exitf(1, "%v", err)
	}
	files := rest

	run := func(name string, b []byte) (string, error) {
		lines, trailer := splitKeepTrailer(b)
		var out strings.Builder
		for idx, line := range lines {
			lineNo := idx + 1
			cur := line
			deleted := false
			for k := range cmds {
				c := &cmds[k]
				if !c.addr.matches(lineNo, cur) {
					continue
				}
				switch c.kind {
				case 's':
					newv, changed := c.sub(cur)
					cur = newv
					if c.print && changed {
						out.WriteString(cur)
						out.WriteByte('\n')
					}
				case 'p':
					out.WriteString(cur)
					out.WriteByte('\n')
				case 'd':
					deleted = true
				}
				if deleted {
					break
				}
			}
			if deleted {
				continue
			}
			if !quiet {
				out.WriteString(cur)
				out.WriteByte('\n')
			}
		}
		result := out.String()
		if !trailer {
			result = strings.TrimSuffix(result, "\n")
		}
		return result, nil
	}

	if len(files) == 0 {
		b, err := readSource(inv, "-")
		if err != nil {
			return command.Exitf(1, "%v", err)
		}
		res, err := run("-", b)
		if err != nil {
			return command.Exitf(1, "%v", err)
		}
		if _, err := io.WriteString(inv.Stdout, res); err != nil {
			return command.Exitf(1, "%v", err)
		}
		return nil
	}

	for _, f := range files {
		abs := inv.Abs(f)
		b, err := afero.ReadFile(inv.FS, abs)
		if err != nil {
			return command.Exitf(1, "%v", err)
		}
		res, err := run(f, b)
		if err != nil {
			return command.Exitf(1, "%v", err)
		}
		if inPlace {
			mode := os.FileMode(0o644)
			if fi, err := inv.FS.Stat(abs); err == nil {
				mode = fi.Mode()
			}
			if err := afero.WriteFile(inv.FS, abs, []byte(res), mode); err != nil {
				return command.Exitf(1, "%v", err)
			}
			continue
		}
		if _, err := io.WriteString(inv.Stdout, res); err != nil {
			return command.Exitf(1, "%v", err)
		}
	}
	return nil
}

// sedCmd is one parsed sed command.
type sedCmd struct {
	addr   sedAddr
	kind   byte // 's', 'd', or 'p'
	re     *regexp.Regexp
	repl   string
	global bool
	print  bool
}

// sub applies the substitution to line, returning the new line and whether any
// replacement was made.
func (c *sedCmd) sub(line string) (string, bool) {
	matches := c.re.FindAllStringSubmatchIndex(line, -1)
	if len(matches) == 0 {
		return line, false
	}
	if !c.global {
		matches = matches[:1]
	}
	var sb strings.Builder
	last := 0
	for _, m := range matches {
		sb.WriteString(line[last:m[0]])
		groups := make([]string, len(m)/2)
		for g := 0; g < len(m)/2; g++ {
			if m[2*g] >= 0 {
				groups[g] = line[m[2*g]:m[2*g+1]]
			}
		}
		sb.WriteString(expandRepl(c.repl, groups))
		last = m[1]
	}
	sb.WriteString(line[last:])
	return sb.String(), true
}

// sedAddr restricts a command to certain lines. A zero value matches everything.
type sedAddr struct {
	re    *regexp.Regexp
	line1 int
	line2 int
}

func (a sedAddr) matches(lineNo int, line string) bool {
	switch {
	case a.re != nil:
		return a.re.MatchString(line)
	case a.line2 > 0:
		return lineNo >= a.line1 && lineNo <= a.line2
	case a.line1 > 0:
		return lineNo == a.line1
	default:
		return true
	}
}

func parseSedScript(s string) ([]sedCmd, error) {
	r := []rune(s)
	pos := 0
	var cmds []sedCmd
	for pos < len(r) {
		for pos < len(r) && (r[pos] == ';' || r[pos] == '\n' || r[pos] == ' ' || r[pos] == '\t') {
			pos++
		}
		if pos >= len(r) {
			break
		}
		c, next, err := parseSedCmd(r, pos)
		if err != nil {
			return nil, err
		}
		cmds = append(cmds, c)
		pos = next
	}
	if len(cmds) == 0 {
		return nil, fmt.Errorf("empty script")
	}
	return cmds, nil
}

func parseSedCmd(r []rune, pos int) (sedCmd, int, error) {
	addr, pos, err := parseSedAddr(r, pos)
	if err != nil {
		return sedCmd{}, pos, err
	}
	for pos < len(r) && (r[pos] == ' ' || r[pos] == '\t') {
		pos++
	}
	if pos >= len(r) {
		return sedCmd{}, pos, fmt.Errorf("missing command")
	}
	cmd := r[pos]
	pos++

	switch cmd {
	case 's':
		if pos >= len(r) {
			return sedCmd{}, pos, fmt.Errorf("unterminated `s' command")
		}
		delim := r[pos]
		pos++
		pat, pos, err := scanSedDelim(r, pos, delim)
		if err != nil {
			return sedCmd{}, pos, err
		}
		repl, pos, err := scanSedDelim(r, pos, delim)
		if err != nil {
			return sedCmd{}, pos, err
		}
		global, ci, printFlag := false, false, false
	Flags:
		for pos < len(r) {
			switch r[pos] {
			case 'g':
				global = true
			case 'i':
				ci = true
			case 'p':
				printFlag = true
			case ';', '\n', ' ', '\t':
				break Flags
			default:
				return sedCmd{}, pos, fmt.Errorf("unknown option to `s': %c", r[pos])
			}
			pos++
		}
		expr := pat
		if ci {
			expr = "(?i)" + expr
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return sedCmd{}, pos, err
		}
		return sedCmd{addr: addr, kind: 's', re: re, repl: repl, global: global, print: printFlag}, pos, nil
	case 'd':
		return sedCmd{addr: addr, kind: 'd'}, pos, nil
	case 'p':
		return sedCmd{addr: addr, kind: 'p'}, pos, nil
	default:
		return sedCmd{}, pos, fmt.Errorf("unknown command: %c", cmd)
	}
}

func parseSedAddr(r []rune, pos int) (sedAddr, int, error) {
	if pos >= len(r) {
		return sedAddr{}, pos, nil
	}
	switch c := r[pos]; {
	case c >= '0' && c <= '9':
		n1, pos := readInt(r, pos)
		if pos < len(r) && r[pos] == ',' {
			pos++
			if pos >= len(r) || r[pos] < '0' || r[pos] > '9' {
				return sedAddr{}, pos, fmt.Errorf("expected line number after ,")
			}
			n2, pos := readInt(r, pos)
			return sedAddr{line1: n1, line2: n2}, pos, nil
		}
		return sedAddr{line1: n1}, pos, nil
	case c == '/':
		s, pos, err := scanSedDelim(r, pos+1, '/')
		if err != nil {
			return sedAddr{}, pos, err
		}
		re, err := regexp.Compile(s)
		if err != nil {
			return sedAddr{}, pos, err
		}
		return sedAddr{re: re}, pos, nil
	default:
		return sedAddr{}, pos, nil
	}
}

func readInt(r []rune, pos int) (int, int) {
	n := 0
	for pos < len(r) && r[pos] >= '0' && r[pos] <= '9' {
		n = n*10 + int(r[pos]-'0')
		pos++
	}
	return n, pos
}

// scanSedDelim reads up to the next unescaped delim. A backslash before the
// delimiter yields a literal delimiter; other backslash escapes are preserved
// so the regexp engine and replacement expander see them intact.
func scanSedDelim(r []rune, pos int, delim rune) (string, int, error) {
	var sb strings.Builder
	for pos < len(r) {
		c := r[pos]
		if c == '\\' && pos+1 < len(r) {
			n := r[pos+1]
			if n == delim {
				sb.WriteRune(delim)
			} else {
				sb.WriteRune('\\')
				sb.WriteRune(n)
			}
			pos += 2
			continue
		}
		if c == delim {
			return sb.String(), pos + 1, nil
		}
		sb.WriteRune(c)
		pos++
	}
	return "", pos, fmt.Errorf("unterminated expression (missing %q)", string(delim))
}

// expandRepl renders a sed replacement string. groups[0] is the whole match.
func expandRepl(repl string, groups []string) string {
	var sb strings.Builder
	rs := []rune(repl)
	for i := 0; i < len(rs); i++ {
		switch c := rs[i]; c {
		case '&':
			if len(groups) > 0 {
				sb.WriteString(groups[0])
			}
		case '\\':
			if i+1 >= len(rs) {
				sb.WriteByte('\\')
				continue
			}
			n := rs[i+1]
			i++
			switch {
			case n >= '0' && n <= '9':
				if idx := int(n - '0'); idx < len(groups) {
					sb.WriteString(groups[idx])
				}
			case n == 'n':
				sb.WriteByte('\n')
			case n == 't':
				sb.WriteByte('\t')
			default:
				sb.WriteRune(n)
			}
		default:
			sb.WriteRune(c)
		}
	}
	return sb.String()
}

// splitKeepTrailer splits content into lines, reporting whether it ended with a
// trailing newline so it can be restored on output.
func splitKeepTrailer(b []byte) ([]string, bool) {
	s := string(b)
	if s == "" {
		return nil, false
	}
	trailer := strings.HasSuffix(s, "\n")
	if trailer {
		s = s[:len(s)-1]
	}
	return strings.Split(s, "\n"), trailer
}

func init() {
	Register("sed", sedCommand)
}
