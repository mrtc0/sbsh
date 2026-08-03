package builtins

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	goawk "github.com/benhoyt/goawk/interp"
	"github.com/benhoyt/goawk/lexer"
	"github.com/benhoyt/goawk/parser"
	"github.com/spf13/afero"
)

// awkCommand runs an AWK program over the input, backed by goawk (a pure-Go
// POSIX awk). The interpreter is confined to the sandbox: it cannot run
// commands (system(), "cmd" | getline), cannot read or write host files
// (getline <file, print >file), and sees an empty ENVIRON. Named input files
// are read from the virtual filesystem and fed as one stdin stream, so field
// and record processing works while the host stays unreachable. Execution is
// bound to ctx, so the sandbox timeout can interrupt a runaway program.
//
//	awk [-F fs] [-v var=value]... 'program' [file...]
//	awk [-F fs] [-v var=value]... -f progfile... [file...]
func awkCommand(ctx context.Context, env *Env) error {
	// awk options must precede the program, so stop scanning at the first operand.
	fs := NewFlagSet().StopAtFirstOperand()
	fieldSepFlag := fs.String("", "-F")
	varFlags := fs.StringList("-v")
	progFileFlags := fs.StringList("-f")
	rest, err := fs.Parse(env.Args)
	if err != nil {
		return err
	}
	fieldSep := *fieldSepFlag
	haveFS := fs.Seen("-F")
	progFiles := *progFileFlags

	var vars []string
	for _, assignment := range *varFlags {
		pair, err := parseAwkVar(assignment)
		if err != nil {
			return err
		}
		vars = append(vars, pair...)
	}

	src, rest, err := awkProgram(env, progFiles, rest)
	if err != nil {
		return err
	}

	stdin, err := awkInput(env, rest)
	if err != nil {
		return err
	}

	prog, err := parser.ParseProgram([]byte(src), nil)
	if err != nil {
		return err
	}

	setVars := make([]string, 0, len(vars)+2)
	if haveFS {
		setVars = append(setVars, "FS", fieldSep)
	}
	setVars = append(setVars, vars...)

	config := &goawk.Config{
		Stdin:  stdin,
		Output: env.Stdout,
		Error:  env.Stderr,
		Vars:   setVars,
		// Fail closed: keep the host unreachable.
		NoExec:       true,
		NoFileReads:  true,
		NoFileWrites: true,
		Environ:      []string{},
	}

	interpreter, err := goawk.New(prog)
	if err != nil {
		return err
	}
	status, err := interpreter.ExecuteContext(ctx, config)
	if err != nil {
		return err
	}
	if status != 0 {
		return exit(status)
	}
	return nil
}

// awkProgram resolves the program source. With -f it concatenates the program
// files from the VFS; otherwise the first positional argument is the program
// and the remaining positionals are returned as input files.
func awkProgram(env *Env, progFiles, rest []string) (src string, files []string, err error) {
	if len(progFiles) > 0 {
		var b strings.Builder
		for _, pf := range progFiles {
			data, err := afero.ReadFile(env.FS, env.Abs(pf))
			if err != nil {
				return "", nil, err
			}
			b.Write(data)
			b.WriteByte('\n')
		}
		return b.String(), rest, nil
	}
	if len(rest) == 0 {
		return "", nil, fmt.Errorf("usage: awk [-F fs] [-v var=value] 'program' [file...]")
	}
	return rest[0], rest[1:], nil
}

// awkInput reads the input files from the VFS and concatenates them into a
// single reader, or reads stdin when no files are named. Files are never handed
// to goawk directly, since that would bypass the virtual filesystem.
func awkInput(env *Env, files []string) (*bytes.Reader, error) {
	if len(files) == 0 {
		b, err := readSource(env, "-")
		if err != nil {
			return nil, err
		}
		return bytes.NewReader(b), nil
	}
	var buf bytes.Buffer
	for _, f := range files {
		b, err := afero.ReadFile(env.FS, env.Abs(f))
		if err != nil {
			return nil, err
		}
		buf.Write(b)
	}
	return bytes.NewReader(buf.Bytes()), nil
}

// parseAwkVar splits a var=value assignment into a goawk name/value pair. Like
// awk's -v, the value's backslash escapes are interpreted.
func parseAwkVar(assignment string) ([]string, error) {
	name, value, ok := strings.Cut(assignment, "=")
	if !ok {
		return nil, fmt.Errorf("-v flag must be in format name=value")
	}
	if unescaped, err := lexer.Unescape(value); err == nil {
		value = unescaped
	}
	return []string{name, value}, nil
}

func init() {
	Register("awk", awkCommand)
}
