// Command nestedcommand shows how a command written in Go runs a script back
// inside the sandbox it is already running in.
//
// It implements "tally", which reports the most common values in a column of a
// CSV file. The counting itself is a pipeline the sandbox already knows how to
// run — cut, sort, uniq — so the command composes it with
// command.Invocation.RunNested instead of reimplementing it in Go.
//
//	go run ./examples/nestedcommand
package main

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/mrtc0/sbsh/sandbox"
	"github.com/mrtc0/sbsh/sandbox/command"
	"github.com/mrtc0/sbsh/sandbox/exec"
)

// tally is a command written outside the sandbox, registered the same way any
// other custom command is.
type tally struct{}

func (tally) Name() string { return "tally" }

func (tally) Description() string { return "count the values in a CSV column, most common first" }

// script is what the nested run evaluates. It reads its inputs from the
// environment rather than being pasted together from the arguments: a value
// interpolated into a script is shell source, so a file named "; rm -rf /" would
// be a second command. A variable is data whatever it holds.
//
// pipefail is set because the pipeline's own status is otherwise the last
// command's, and it is the first one that reads the file.
const script = `
set -o pipefail
cut -d, -f"$TALLY_FIELD" -- "$TALLY_FILE" | sort | uniq -c | sort -rn
`

// Run parses the arguments and hands the work to the sandbox.
func (t tally) Run(ctx context.Context, inv *command.Invocation) error {
	if len(inv.Args) != 2 {
		return command.Exit(2, "usage: tally FIELD FILE")
	}
	field, err := strconv.Atoi(inv.Args[0])
	if err != nil || field < 1 {
		return command.Exitf(2, "field must be a positive number, not %q", inv.Args[0])
	}

	res, err := inv.RunNested(ctx, command.NestedRequest{
		Script: script,
		// No Dir: the child starts where the calling script stands, so a relative
		// file argument means what it means to the caller.
		//
		// A child inherits none of the caller's environment beyond HOME and PWD,
		// so everything the script reads is named here.
		Env: []string{
			"TALLY_FIELD=" + strconv.Itoa(field),
			"TALLY_FILE=" + inv.Args[1],
		},
		// Both limits only ever tighten. Whatever the sandbox allows still
		// applies, and the caller's own deadline is still in force.
		Timeout:     5 * time.Second,
		OutputLimit: 64 << 10,
	})
	if err != nil {
		// An error means the request could not be made sense of, or the sandbox
		// itself failed — never that the script exited non-zero.
		return command.Exitf(1, "%v", err)
	}

	// What the child did is read from the result, not from its stderr. Every
	// ending has an outcome of its own, so the command can say something useful
	// about each without matching on the wording of a diagnostic.
	switch {
	case res.OK():
	case res.Outcome != exec.OutcomeCompleted:
		return command.Exitf(1, "counting %s: %s", inv.Args[1], res.Outcome)
	default:
		// The pipeline ran and failed on its own — an unreadable file, most
		// likely. Its diagnostic is worth passing on.
		return command.Exitf(res.ExitCode, "counting %s: %s",
			inv.Args[1], strings.TrimSpace(res.Stderr))
	}
	if res.Truncated {
		// Truncation is independent of the outcome: the run completed, and the
		// output is still incomplete.
		return command.Exit(1, "too many distinct values to count")
	}

	// The child's captured output is the command's to write out, formatted the
	// way this command wants it rather than the way uniq -c happens to.
	for line := range strings.Lines(res.Stdout) {
		count, value, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		if _, err := fmt.Fprintf(inv.Stdout, "%s\t%s\n", strings.TrimSpace(value), count); err != nil {
			return command.Exitf(1, "write: %v", err)
		}
	}
	return command.Exit(0)
}

func main() {
	ctx := context.Background()

	sb, err := sandbox.New(ctx, sandbox.WithCommand(tally{}))
	if err != nil {
		log.Fatal(err)
	}
	defer sb.Close()

	// tally is a command like any other: it takes the script's working directory,
	// and its output pipes onward.
	res, err := sb.Exec(ctx, `
mkdir -p /tmp/data
cat > /tmp/data/orders.csv <<'EOF'
1,emea,10
2,amer,20
3,emea,30
4,apac,40
5,emea,50
EOF
cd /tmp/data
tally 2 orders.csv | head -n 2
`, nil)
	if err != nil {
		log.Fatal(err) // a sandbox-level failure, not a script failure
	}
	fmt.Printf("exit=%d\n%s", res.ExitCode, res.Stdout)

	// A nested run that fails is not an error for the host either: the pipeline
	// exits non-zero, tally reports that status with a message of its own, and
	// the top-level result is an ordinary completion.
	res, err = sb.Exec(ctx, "tally 2 /tmp/data/missing.csv", nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("missing file: exit=%d outcome=%s stderr=%s",
		res.ExitCode, res.Outcome, res.Stderr)
}
