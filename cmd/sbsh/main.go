package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mrtc0/sbsh/internal/repl"
	"github.com/mrtc0/sbsh/sandbox"
	"github.com/mrtc0/sbsh/version"
	"github.com/spf13/cobra"
)

func main() {
	os.Exit(run())
}

func run() int {
	// The two signals ask for different things, so they travel separately.
	//
	// SIGTERM asks the process to stop: it ends the root context, which cancels the
	// running script and takes the REPL out of its loop.
	//
	// SIGINT asks for the script to stop and nothing further. Ending the root
	// context would answer it by breaking every later Exec, so it goes to the
	// Runner on its own channel and the context the sandbox was built with stays
	// usable.
	//
	// Neither path calls os.Exit, so the deferred Close below still runs and the
	// Wasm runtime and mount handles are released.
	ctx, stopTerm := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stopTerm()

	interrupts, stopInt := notifyInterrupts()
	defer stopInt()

	err := newRootCmd(interrupts).ExecuteContext(ctx)

	// A process asked to stop reports that it stopped, whatever the script it
	// happened to be running was going to report. Without this, SIGTERM during -c
	// would surface as the script's own 128 + SIGINT, because a cancelled context
	// is all the sandbox has to go on.
	if ctx.Err() != nil {
		return exitCodeTerminated
	}

	var ee *exitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		return ee.code
	default:
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
}

// exitCodeTerminated is the process's own exit code once SIGTERM has been
// honoured, following the 128 + signal convention the sandbox uses for a script
// it stopped.
const exitCodeTerminated = 128 + 15 // SIGTERM

// notifyInterrupts reports SIGINT on a channel that carries no signal type, so
// that internal/repl does not have to know about os/signal to be interruptible or
// to be tested.
//
// One slot of buffer holds one pending interrupt. A second arriving before the
// first is read says the same thing, so dropping it loses nothing, and the drop
// is what keeps the forwarding goroutine from blocking on a Runner that is not
// currently running a script.
func notifyInterrupts() (<-chan struct{}, func()) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt)

	out := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-sigs:
				select {
				case out <- struct{}{}:
				default:
				}
			case <-done:
				return
			}
		}
	}()

	return out, func() {
		signal.Stop(sigs)
		close(done)
	}
}

// exitError carries a non-zero exit code from a script back to main
// without calling os.Exit inside RunE, so deferred cleanup still runs.
type exitError struct{ code int }

func (e *exitError) Error() string { return fmt.Sprintf("exit code %d", e.code) }

// flags holds what the command line asked for, before it is turned into
// sandbox options.
type flags struct {
	script    string
	mounts    []string
	denyPaths []string
	allowNet  []string
}

func newRootCmd(interrupts <-chan struct{}) *cobra.Command {
	var f flags

	cmd := &cobra.Command{
		Use:           "sbsh",
		Short:         "Sandboxed shell",
		Version:       version.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := f.options()
			if err != nil {
				return err
			}

			sb, err := sandbox.New(cmd.Context(), opts...)
			if err != nil {
				return err
			}
			defer sb.Close()

			runner := repl.New(os.Stdin, os.Stdout, os.Stderr, repl.WithInterrupts(interrupts))

			if f.script != "" {
				if code := runner.Run(cmd.Context(), sb, f.script); code != 0 {
					return &exitError{code: code}
				}
				return nil
			}

			if code := runner.Loop(cmd.Context(), sb); code != 0 {
				return &exitError{code: code}
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&f.script, "command", "c", "", "Run a script once and exit")
	cmd.Flags().StringArrayVar(&f.mounts, "mount", nil, "Specify mounts in the form HOST:VPATH[:ro] (VPATH is the virtual path, multiple allowed)")
	cmd.Flags().StringArrayVar(&f.denyPaths, "deny-path", nil,
		"Refuse access to paths matching PATTERN, on top of the mounts. \"*\" stays within one path segment, \"**\" spans any number of them, and a pattern that does not start with \"/\" applies at any depth. Denying a directory denies everything below it (multiple allowed)")
	cmd.Flags().StringArrayVar(&f.allowNet, "allow-net", nil,
		"Allow network access to a host name, a \"*.\" wildcard host name, an IP address or a CIDR block. Without this flag the sandbox has no network access (multiple allowed)")

	return cmd
}

func (f flags) options() ([]sandbox.Option, error) {
	var opts []sandbox.Option
	for _, m := range f.mounts {
		parts := strings.Split(m, ":")
		switch {
		case len(parts) == 3 && parts[2] == "ro":
			opts = append(opts, sandbox.WithHostMountRO(parts[0], parts[1]))
		case len(parts) == 2:
			opts = append(opts, sandbox.WithHostMountRW(parts[0], parts[1]))
		default:
			return nil, fmt.Errorf("invalid --mount %q (want HOST:VPATH[:ro])", m)
		}
	}
	if len(f.denyPaths) > 0 {
		opts = append(opts, sandbox.WithDenyPaths(f.denyPaths...))
	}
	if len(f.allowNet) > 0 {
		opts = append(opts, sandbox.WithNetworkAllow(f.allowNet...))
	}
	return opts, nil
}
