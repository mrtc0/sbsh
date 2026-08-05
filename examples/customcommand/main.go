// Command customcommand shows how to add a command to sbsh from Go.
//
// It implements "wordfreq", which counts word occurrences in its file arguments
// or in its standard input and prints them most frequent first. The command is
// registered with sandbox.WithCommand and is then a command like any other: the
// script below redirects into it, pipes out of it, and takes its exit status.
//
//	go run ./examples/customcommand
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"sort"
	"strings"

	"github.com/mrtc0/sbsh/sandbox"
	"github.com/mrtc0/sbsh/sandbox/command"
)

// wordFreq is a command written outside the sandbox. The three methods are the
// whole interface.
type wordFreq struct{}

func (wordFreq) Name() string { return "wordfreq" }

func (wordFreq) Description() string { return "count word occurrences, most frequent first" }

// Run reads each argument as a file in the sandbox filesystem, or standard input
// when there is no argument or the argument is "-".
//
// Every return is a command.Exit or command.Exitf: the command picks its exit
// status, and attaches a message only when the caller should be shown one.
func (wordFreq) Run(_ context.Context, inv *command.Invocation) error {
	counts := map[string]int{}

	sources := inv.Args
	if len(sources) == 0 {
		sources = []string{"-"}
	}
	for _, name := range sources {
		r := inv.Stdin
		if name != "-" {
			// inv.FS is the sandbox filesystem: mounts resolved, deny patterns in
			// force. inv.Abs resolves a relative argument against the script's
			// working directory.
			f, err := inv.FS.Open(inv.Abs(name))
			if err != nil {
				// A message travels with the status: the sandbox prints it as
				// "wordfreq: ..." on stderr and the script exits 1.
				return command.Exitf(1, "%v", err)
			}
			defer f.Close()
			r = f
		}
		if err := count(r, counts); err != nil {
			return command.Exitf(1, "%s: %v", name, err)
		}
	}

	if len(counts) == 0 {
		// A status of the command's own, the way grep reports "no match". No
		// message: nothing went wrong, so there is nothing to show.
		return command.Exit(1)
	}

	words := make([]string, 0, len(counts))
	for w := range counts {
		words = append(words, w)
	}
	sort.Slice(words, func(i, j int) bool {
		if counts[words[i]] != counts[words[j]] {
			return counts[words[i]] > counts[words[j]]
		}
		return words[i] < words[j]
	})

	out := bufio.NewWriter(inv.Stdout)
	for _, w := range words {
		if _, err := fmt.Fprintf(out, "%d\t%s\n", counts[w], w); err != nil {
			return command.Exitf(1, "write: %v", err)
		}
	}
	if err := out.Flush(); err != nil {
		return command.Exitf(1, "write: %v", err)
	}
	// Success is reported the same way as failure: a status, and no message.
	return command.Exit(0)
}

func count(r io.Reader, counts map[string]int) error {
	sc := bufio.NewScanner(r)
	sc.Split(bufio.ScanWords)
	for sc.Scan() {
		word := strings.ToLower(strings.Trim(sc.Text(), ".,:;!?\"'()[]"))
		if word != "" {
			counts[word]++
		}
	}
	return sc.Err()
}

func main() {
	ctx := context.Background()

	sb, err := sandbox.New(ctx, sandbox.WithCommand(wordFreq{}))
	if err != nil {
		log.Fatal(err)
	}
	defer sb.Close()

	for _, cmd := range sb.Commands() {
		fmt.Printf("registered: %s — %s\n", cmd.Name(), cmd.Description())
	}

	res, err := sb.Exec(ctx, `
cat > /tmp/notes.txt <<'EOF'
the quick brown fox jumps over the lazy dog
the dog barks and the fox runs
EOF
wordfreq /tmp/notes.txt | head -n 3
`, nil)
	if err != nil {
		log.Fatal(err) // a sandbox-level failure, not a script failure
	}
	fmt.Printf("exit=%d\n%s", res.ExitCode, res.Stdout)

	// Standard input works the same way, and so does the command's own exit
	// status: empty input has nothing to count, so wordfreq exits 1 with nothing
	// on stderr — a status of its own is not a diagnostic.
	res, err = sb.Exec(ctx, "wordfreq", strings.NewReader(""))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("empty input: exit=%d stderr=%q\n", res.ExitCode, res.Stderr)

	// A failure the caller should be told about carries its message with the
	// status, and the sandbox prints it as "wordfreq: ...".
	res, err = sb.Exec(ctx, "wordfreq /tmp/missing.txt", nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("missing file: exit=%d stderr=%s", res.ExitCode, res.Stderr)
}
