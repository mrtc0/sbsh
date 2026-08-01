package sandbox

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/mrtc0/sh/v3/interp"
	"github.com/mrtc0/sh/v3/syntax"

	"github.com/mrtc0/sbsh/sandbox/builtins"
	"github.com/mrtc0/sbsh/sandbox/command"
)

// commandName is what a command may be called: a plain word, matched against
// the first word of a command line. There is no PATH to look a name up on, so a
// name that needs quoting to be typed could only ever be confusing.
var commandName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// WithCommand registers custom commands with the sandbox. A registered command
// is invoked, dispatched, and reported on exactly like a builtin: it appears in
// pipelines, its streams can be redirected, and its exit status is the script's.
//
// Registration is per sandbox — there is no process-wide registry to add to —
// so two sandboxes in one program can offer different commands, and a test can
// register a command without leaking it into the next test.
//
// It fails, and so does [New], when a command is nil, has a name that is not a
// plain word, has an empty description, would shadow a builtin or a name the
// shell handles itself (such as "cd" or "echo"), or repeats a name already
// registered. Each of those is a command that would silently never run, or
// would silently displace another, which is worth a construction error rather
// than a puzzle later.
func WithCommand(cmds ...command.Command) Option {
	return func(s *Sandbox) error {
		for _, cmd := range cmds {
			if err := s.addCommand(cmd); err != nil {
				return err
			}
		}
		return nil
	}
}

func (s *Sandbox) addCommand(cmd command.Command) error {
	if cmd == nil {
		return fmt.Errorf("registering command: command is nil")
	}

	name := cmd.Name()
	switch {
	case name == "":
		return fmt.Errorf("registering command: name is empty")
	case !commandName.MatchString(name):
		return fmt.Errorf("registering command %q: name must match %s", name, commandName)
	case strings.TrimSpace(cmd.Description()) == "":
		return fmt.Errorf("registering command %q: description is empty", name)
	case builtins.Registered(name):
		return fmt.Errorf("registering command %q: a builtin already has that name", name)
	case interp.IsBuiltin(name), syntax.IsKeyword(name):
		// A keyword is not a command at all: a script starting with "if" or
		// "function" fails to parse, and "time" measures what follows it. Either
		// way the registered command could never run.
		return fmt.Errorf("registering command %q: the shell handles that name itself", name)
	}
	if _, ok := s.commands[name]; ok {
		return fmt.Errorf("registering command %q: already registered", name)
	}

	if s.commands == nil {
		s.commands = map[string]command.Command{}
	}
	s.commands[name] = cmd
	return nil
}

// Commands returns the custom commands registered on the sandbox, sorted by
// name. It is what a host renders its own help or listing from; the sandbox
// itself has no help command.
func (s *Sandbox) Commands() []command.Command {
	out := make([]command.Command, 0, len(s.commands))
	for _, cmd := range s.commands {
		out = append(out, cmd)
	}
	slices.SortFunc(out, func(a, b command.Command) int {
		return strings.Compare(a.Name(), b.Name())
	})
	return out
}
