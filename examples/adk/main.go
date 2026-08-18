// Command adk runs an agent written with the Google Agent Development Kit as a
// command inside the sandbox.
//
// The agent is registered with sandbox.WithCommand, so a script invokes it the
// way it invokes any other command:
//
//	my-agent 'how many rows does /work/sales.csv have?'
//
// It answers by writing code and running it, and the only tool it has for that
// is run_shell — which is command.Invocation.RunNested. So every script the
// model writes is evaluated by the sandbox that is already running the agent:
// mounts, deny patterns, timeouts and output limits reach the model's code
// because there is no other path for it to take. The model gets the sandbox's
// python and shell tools and nothing else, and no pip to change that.
//
// Invocations share one conversation. The agent is built fresh for each call,
// but the session service outlives them, so the second question arrives with
// the first one still in the history.
//
// What the sandbox does not bound is the agent's own call to the Gemini API:
// that is an ordinary HTTP request from the host process. The sandbox is a
// boundary around what the model may do to this machine, not around who the
// host talks to. Passing Invocation.HTTP to genai.ClientConfig would put the
// model call behind the network policy too; this example keeps it plain so the
// two boundaries stay easy to tell apart.
//
// This example is a module of its own, so the ADK dependency tree stays out of
// sbsh's:
//
//	cd examples/adk && GOOGLE_API_KEY=... go run .
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/gemini"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/genai"

	"github.com/mrtc0/sbsh/sandbox"
	"github.com/mrtc0/sbsh/sandbox/command"
)

// instruction tells the model what it has to work with and what to do with a
// tool result. Three things are worth stating: code is how it answers, the
// filesystem is not the machine's, and a result is to be read rather than
// assumed. A model told none of them tends to answer from the shape of the
// question, and to reach for pip when a script fails.
const instruction = `
You answer a question by writing code and running it with the run_shell tool.

The sandbox has python (CPython 3.14) and the usual shell tools: cut, sort,
uniq, awk, jq, grep, wc. Pass short code with python -c, or write a script into
/tmp with a heredoc and run it. There is no pip and no network, so the standard
library is all you have.

Only mounted paths exist, and some paths are refused outright.

Always read the tool result. A non-zero exit_code means your code failed and
stderr says why; fix it and run again. An outcome of "denied" means the sandbox
refused the script — say so plainly and do not look for another route to what it
refused.

Never state a number you have not read out of a script's output.`

// shellArgs is what the model fills in to call run_shell. ADK infers the tool's
// schema from this type, so the field name and its json tag are what the model
// is shown.
type shellArgs struct {
	Script string `json:"script"`
}

// shellResult carries the whole ending, not just the output. exit_code and
// outcome are separate because they answer different questions — the status the
// script chose, and why the run ended at all — and a model that can see both
// can tell "grep found nothing" from "the sandbox stopped you".
type shellResult struct {
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	ExitCode  int    `json:"exit_code"`
	Outcome   string `json:"outcome"`
	Truncated bool   `json:"truncated,omitempty"`
}

// The conversation every invocation joins. One sandbox is one shell session, so
// one agent conversation goes with it.
const (
	appName   = "sbsh-adk-example"
	userID    = "sbsh"
	sessionID = "shell"
)

// AgentCommand takes a question, answers it by running code in the sandbox, and
// exits.
//
// The two fields are what outlives a single call: the model, and the history.
// Everything else an invocation needs comes from its Invocation, and is built
// per call.
type AgentCommand struct {
	llm      model.LLM
	sessions session.Service
}

// NewAgentCommand builds the command with a conversation of its own. The
// sessions field has no useful zero value, which is why this is a constructor
// rather than a struct literal in main.
func NewAgentCommand(llm model.LLM) AgentCommand {
	return AgentCommand{llm: llm, sessions: session.InMemoryService()}
}

func (AgentCommand) Name() string { return "my-agent" }

func (AgentCommand) Description() string {
	return "answer a question by writing code and running it in the sandbox"
}

// Run turns one question into one agent invocation.
//
// The tool, the agent and the runner are built here rather than in main because
// the tool closes over inv, and nothing in an Invocation outlives the call: its
// streams belong to the shell, which may already be piping them into the next
// command of a pipeline. The history is not in any of them — it is in
// c.sessions — so rebuilding them every call costs nothing but the wiring.
func (c AgentCommand) Run(ctx context.Context, inv *command.Invocation) error {
	if len(inv.Args) != 1 {
		return command.Exit(2, "usage: my-agent QUESTION")
	}

	shellTool, err := functiontool.New(functiontool.Config{
		Name:        "run_shell",
		Description: "Run a shell script in the sandbox and return its output and exit status.",
	}, func(actx agent.Context, args shellArgs) (shellResult, error) {
		// agent.Context is a context.Context, so the nested run hangs from the
		// invocation the tool call belongs to: cancel the agent and the script
		// stops with it.
		res, err := inv.RunNested(actx, command.NestedRequest{
			Script: args.Script,
			// No Dir: the model's relative paths mean what they mean to the
			// script that called my-agent.
			//
			// Both limits only tighten. They bound one script the model wrote,
			// which is a smaller thing than the whole conversation, and the
			// sandbox's own limits still apply on top. Python compiles what it
			// imports on every run, so a script gets more time than a pipeline
			// of shell tools would need.
			Timeout:     30 * time.Second,
			OutputLimit: 32 << 10,
		})
		if err != nil {
			// The request could not be made sense of — an empty script, most
			// likely. Returning the error would fail the tool call and end the
			// invocation, so it goes back as a result the model can act on.
			return shellResult{
				Stderr:   err.Error(),
				ExitCode: res.ExitCode,
				Outcome:  res.Outcome.String(),
			}, nil
		}

		// A trace on stderr, so the transcript shows what the model actually
		// ran. sb.Exec captures it; main prints it.
		fmt.Fprintf(inv.Stderr, "%s: run_shell:\n%s\n\t-> exit=%d outcome=%s\n",
			inv.Name, indent(args.Script), res.ExitCode, res.Outcome)

		return shellResult{
			Stdout:    res.Stdout,
			Stderr:    res.Stderr,
			ExitCode:  res.ExitCode,
			Outcome:   res.Outcome.String(),
			Truncated: res.Truncated,
		}, nil
	})
	if err != nil {
		return command.Exitf(1, "build tool: %v", err)
	}

	// An ADK name is an identifier, so the command's name is spelled my_agent
	// here. Description is what a parent agent would read to decide whether to
	// delegate; this agent has no parent, so it only has to agree with the
	// command's own description.
	a, err := llmagent.New(llmagent.Config{
		Name:        "my_agent",
		Model:       c.llm,
		Description: "Answers a question by writing code and running it in the sandbox.",
		Instruction: instruction,
		Tools:       []tool.Tool{shellTool},
	})
	if err != nil {
		return command.Exitf(1, "build agent: %v", err)
	}

	// The runner is per call and the session service is not, which is what makes
	// consecutive invocations one conversation: the history lives in c.sessions
	// under sessionID, and each runner picks it back up. AutoCreateSession is
	// what creates it on the first call.
	//
	// Two invocations of the same pipeline run at the same time and would write
	// into the same history. Fine for a command a person types; a host that
	// wants a conversation per pipeline stage would derive the session ID from
	// something in the invocation instead.
	r, err := runner.New(runner.Config{
		AppName:           appName,
		Agent:             a,
		SessionService:    c.sessions,
		AutoCreateSession: true,
	})
	if err != nil {
		return command.Exitf(1, "build runner: %v", err)
	}

	question := genai.NewContentFromText(inv.Args[0], genai.RoleUser)
	var answer strings.Builder
	for event, err := range r.Run(ctx, userID, sessionID, question, agent.RunConfig{
		StreamingMode: agent.StreamingModeNone,
	}) {
		if err != nil {
			return command.Exitf(1, "%v", err)
		}
		// Events also carry the model's function calls and their responses. The
		// answer is the final one with text in it.
		if !event.IsFinalResponse() || event.Content == nil {
			continue
		}
		for _, part := range event.Content.Parts {
			answer.WriteString(part.Text)
		}
	}

	text := strings.TrimSpace(answer.String())
	if text == "" {
		// The agent ran and said nothing. Nothing failed, so this is a status of
		// the command's own, the way grep reports no match.
		return command.Exit(1, "the agent produced no answer")
	}
	if _, err := fmt.Fprintln(inv.Stdout, text); err != nil {
		return command.Exitf(1, "write: %v", err)
	}
	return command.Exit(0)
}

// indent offsets a script in the trace, so a heredoc spanning several lines
// still reads as one thing the model ran.
func indent(script string) string {
	lines := strings.Split(strings.TrimSpace(script), "\n")
	for i, line := range lines {
		lines[i] = "\t| " + line
	}
	return strings.Join(lines, "\n")
}

func main() {
	ctx := context.Background()

	key := os.Getenv("GOOGLE_API_KEY")
	if key == "" {
		log.Fatal("GOOGLE_API_KEY is not set")
	}
	llm, err := gemini.NewModel(ctx, "gemini-flash-latest", &genai.ClientConfig{APIKey: key})
	if err != nil {
		log.Fatalf("create model: %v", err)
	}

	dir, err := os.MkdirTemp("", "sbsh-adk-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if err := writeFixtures(dir); err != nil {
		log.Fatal(err)
	}

	sb, err := sandbox.New(ctx,
		// The only host directory that exists as far as the agent is concerned,
		// and it cannot write to it. /tmp is the sandbox's own memory, so the
		// model still has somewhere to put a script.
		sandbox.WithHostMountRO(dir, "/work"),
		sandbox.WithCommand(NewAgentCommand(llm)),
		// The default 30s bounds one script comfortably and an agent turn not
		// at all: a nested run cannot outlive the execution it hangs from, so a
		// deadline set for scripts would cut the agent off mid-conversation.
		sandbox.WithTimeout(5*time.Minute),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer sb.Close()

	// A question the agent has to read a file to answer. It has no idea what is
	// in /work until it looks.
	run(ctx, sb, `my-agent 'What columns does /work/sales.csv have, and how many data rows?'`)

	// The same conversation continued. "that file" and "those columns" only mean
	// something because the first exchange is still in the history — and the
	// agent still has to run code to answer.
	run(ctx, sb, `my-agent 'Using that file, which region has the highest total amount? Show the total per region.'`)
}

func run(ctx context.Context, sb *sandbox.Sandbox, script string) {
	fmt.Printf("\n$ %s\n", script)
	res, err := sb.Exec(ctx, script, nil)
	if err != nil {
		log.Fatal(err) // a sandbox-level failure, not a script failure
	}
	if res.Stderr != "" {
		fmt.Print(res.Stderr)
	}
	fmt.Printf("exit=%d\n%s", res.ExitCode, res.Stdout)
}

func writeFixtures(dir string) error {
	const sales = `id,region,amount
1,emea,120
2,amer,300
3,emea,180
4,apac,90
5,amer,60
6,apac,240
`
	return os.WriteFile(filepath.Join(dir, "sales.csv"), []byte(sales), 0o600)
}
