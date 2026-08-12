# sbsh

sbsh is a sandboxed shell/runtime for AI agents.
It gives agents a constrained environment for safe local work on mounted files and approved network destinations.
Instead of exposing the host shell directly, sbsh provides a predictable execution boundary for investigation, transformation, and lightweight automation.
It is designed for embedding into agentic tools and AI systems that need controlled shell-like execution.

`sbsh` runs shell scripts — pipelines, redirections, globs, heredocs, functions, and Python — without ever spawning a host process.
Commands are Go functions against a virtual filesystem; Python is CPython compiled to WebAssembly and run under [wazero](https://wazero.io).
There is no `fork`, no `exec`, and no `PATH` lookup on the host.

Two things reach the host, and both are closed by default:

| Boundary | Opened by | Enforced by |
|---|---|---|
| Filesystem | host directory mounts | `os.Root` (`openat2` + `RESOLVE_BENEATH` semantics) |
| Network | an allow list of hosts, IPs, and CIDRs | a policy-checked `http.Client` |

Everything else has no route out by construction: no subprocess is ever created,
and the Wasm module is instantiated with a filesystem mount and nothing else.

```console
$ sbsh --mount ./work:/work --deny-path '**/.env' --allow-net '*.githubusercontent.com'
sbsh REPL (Ctrl-D to exit)
$ ls /work
.env
data.csv
$ cut -d, -f1 /work/data.csv | sort -r
b
a
$ cat /work/.env
cat: open /work/.env: permission denied
(exit code 1)
$ python -c 'import sys; print(sys.version.split()[0])'
3.14.6
```

## Install

```sh
go install github.com/mrtc0/sbsh/cmd/sbsh@latest
```

Requirements: Go 1.25+, and nothing else.

### CLI

```
sbsh [flags]

  -c, --command string        Run a script once and exit
      --mount stringArray     HOST:VPATH[:ro] — expose a host directory at a virtual path
      --deny-path stringArray Refuse access to paths matching PATTERN
      --allow-net stringArray Allow network access to a host name, "*." wildcard, IP, or CIDR
      --timeout string        Stop a script that runs longer than this, e.g. "500ms", "30s", "1m"
                              ("0" removes the deadline; default "30s")
      --output-limit string   Capture at most this many bytes of stdout and of stderr
                              per script (default 4194304, 4 MiB)
  -v, --version               Print the version
```

With no `-c`, `sbsh` starts a REPL. On a terminal it provides line editing and
in-session history; when stdin is a pipe it reads line by line, so an agent can
feed it a script directly. `-c` runs one script and exits with the script's
status.

`SIGINT` stops the running script and leaves the REPL at its prompt; `-c` exits
`130`. `SIGTERM` stops the script and the process, which exits `143`. Either way
the sandbox is closed on the way out. Pressing Ctrl-C at a terminal discards the
line being typed and draws a fresh prompt; only Ctrl-D ends the session.
Interrupting a script that is already running still takes a `SIGINT` from
elsewhere — see Not implemented below.

## Using the Go API

A `Sandbox` is one long-lived shell session. Working directory, variables, and
functions persist across `Exec` calls, and calls are serialized.

```go
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/mrtc0/sbsh/sandbox"
)

func main() {
	ctx := context.Background()

	sb, err := sandbox.New(ctx,
		sandbox.WithHostMountRW("./work", "/work"),
		sandbox.WithDenyPaths("**/.env", "/work/secrets"),
		sandbox.WithNetworkAllow("*.githubusercontent.com"),
		sandbox.WithEnv("TZ", "UTC"),
		sandbox.WithTimeout(10*time.Second),
		sandbox.WithOutputLimit(1<<20),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer sb.Close()

	res, err := sb.Exec(ctx, `cd /work && wc -l data.csv && python -c 'print(1+1)'`, nil)
	if err != nil {
		log.Fatal(err) // a sandbox-level failure, not a script failure
	}
	fmt.Printf("exit=%d truncated=%v\n%s", res.ExitCode, res.Truncated, res.Stdout)
}
```

### Execution results

`sandbox.Result` is `exec.Result` from `sandbox/exec`: one shape for every
execution in the sandbox, so an execution a command starts from inside the
sandbox reports back the same way a host's `Exec` does.

A result is always populated, including for the endings that come with an
`error`, and `Result.Outcome` says why the execution ended. Machine meaning lives
in that field, not in the wording of a stderr line; stderr stays what a person
reads.

| Outcome | Exit code | Meaning | `error` |
|---|---|---|---|
| `OutcomeCompleted` | the request's own | Ran to completion and picked its own status | nil |
| `OutcomeNotFound` | 127 | The run ended on an unresolved command name | nil |
| `OutcomeDenied` | 126 | The sandbox refused the request; the reason is on stderr | nil |
| `OutcomeTimedOut` | 137 | The deadline passed and the run was stopped | nil |
| `OutcomeCanceled` | 130 | The caller's context was cancelled | nil |
| `OutcomeInvalid` | 2 | The request could never become a run — a script that does not parse | non-nil |
| `OutcomeInternal` | 125 | The sandbox itself failed; the status says nothing | non-nil |
| `OutcomeUnsupported` | 126 | The runtime has no way to carry the request out; stderr says what to use instead | nil |

An `error` therefore means one of two things only: the request could not be made
sense of, or the sandbox is at fault. Everything else — a failing script, an
unknown command, a refusal, a limit the caller asked for — is a normal outcome
with a nil `error`. `Result.OK()` is the one check for "did this work", and
`Result.Stopped()` for "a limit ended it".

`Result.Truncated` is independent of the outcome: a run that wrote past the
output limit still completed, so a caller that must see everything has to treat
truncation as a failure of its own.

A denial a command runs into while it works — a path `WithDenyPaths` covers, a
destination the network policy does not allow — is not `OutcomeDenied`. That is
the command's own failure, reported the way it reports any other: a status and a
diagnostic. `OutcomeDenied` is for a request the sandbox would not start.

`OutcomeUnsupported` is the neighbouring case: a request this runtime *cannot*
carry out rather than one it refuses. Calling `Exec` from inside the sandbox is
the example — a single shell session cannot re-enter itself, and no permission is
being decided — so a caller reporting a refusal to a user does not tell them a
policy stopped them when nothing did.

A command running inside the sandbox reports back with the same contract, which
is why classification lives in `sandbox/exec` rather than in `Exec` — see
[Nested execution](#nested-execution).

### Options

| Option | Effect |
|---|---|
| `WithHostMountRO(hostDir, vpath)` | Mount a host directory read-only |
| `WithHostMountRW(hostDir, vpath)` | Mount a host directory read-write |
| `WithMountRO(vpath, afero.Fs)` | Mount any `afero.Fs` read-only |
| `WithMountRW(vpath, afero.Fs)` | Mount any `afero.Fs` read-write |
| `WithDenyPaths(patterns...)` | Refuse access to matching paths, on top of the mounts |
| `WithNetworkAllow(entries...)` | Allow outbound access to the listed destinations |
| `WithEnv(k, v)` | Add an environment variable |
| `WithTimeout(d)` | Wall-clock limit per `Exec` (default 30s) |
| `WithOutputLimit(n)` | Cap on captured stdout and stderr (default 4 MiB) |
| `WithCommand(cmds...)` | Register commands written in Go |

### Custom commands

A host that needs a command sbsh does not ship writes it in Go and registers it
on the sandbox. A registered command is dispatched exactly like a builtin: it
appears in pipelines, its streams can be redirected, its exit status is the
script's, and an unregistered name still prints `command not found`. It is not a
second kind of command — a builtin is a `command.RunFunc` receiving the same
`command.Invocation`.

The interface is three methods:

```go
type Command interface {
	Name() string
	Description() string
	Run(ctx context.Context, inv *command.Invocation) error
}
```

`command.New(name, description, fn)` builds one from a function when the command
holds no state:

```go
sb, err := sandbox.New(ctx,
	sandbox.WithCommand(command.New("slugify", "lower-case and hyphenate a line",
		func(_ context.Context, inv *command.Invocation) error {
			b, err := io.ReadAll(inv.Stdin)
			if err != nil {
				return command.Exitf(1, "read: %v", err)
			}
			slug := strings.Join(strings.Fields(strings.ToLower(string(b))), "-")
			if _, err := fmt.Fprintln(inv.Stdout, slug); err != nil {
				return command.Exitf(1, "write: %v", err)
			}
			return command.Exit(0)
		})),
)
```

`Invocation` is everything the command gets for one call, and nothing in it
mentions the shell:

| Field | What it is |
|---|---|
| `Name` | The command's own name, for prefixing its diagnostics |
| `Args` | The arguments, without the name |
| `Dir` | The working directory; `inv.Abs(p)` resolves an argument against it |
| `Stdin`, `Stdout`, `Stderr` | The streams the shell wired up — a pipe, a redirect, or captured output |
| `Env` | The environment as the script sees it, including `LANG=C mycmd` assignments: `Lookup` for one variable, `All` for every one, `inv.Getenv` when unset and empty need not differ |
| `FS` | The sandbox filesystem, mounts resolved and deny patterns in force |
| `HTTP` | The policy-checked client, `nil` when no network was allowed |
| `Python` | The sandbox's Python interpreter, the one the `python` command runs on |
| `Nested` | The runtime behind `inv.RunNested`, which runs a script back inside the sandbox — see [Nested execution](#nested-execution) |

Nothing in it is valid after the call returns: the streams belong to the shell,
which may be piping them into the next command.

`FS`, `HTTP` and `Python` are the only ways out, and they are exactly the ones
the builtins have. A command that reaches for `os` or `net/http` directly steps
outside the sandbox, and registering it does not make that safe.

A command reports back one way, whether it succeeded or not: it returns
`command.Exit` or `command.Exitf`, picking its own status and attaching a message
only when the caller should be shown one.

```go
return command.Exit(0)                     // done, nothing to say
return command.Exit(1)                     // a status of its own, the way grep reports "no match"
return command.Exit(2, "too many files")   // prints "name: too many files" on stderr, exits 2
return command.Exitf(1, "read: %v", err)   // the same, with the message formatted
```

The message travels with the status, so a command need not write the diagnostic
it fails on to `inv.Stderr` itself — that is for what it reports while it keeps
going. Do not wrap the result either: only the message the `ExitError` carries is
printed, so `fmt.Errorf("bad usage: %w", command.Exit(2))` exits 2 silently. Use
`command.Exitf` to build the message instead.

Returning any other error is outside the contract. The sandbox has no status to
go by, so it falls back to printing the error and exiting `1`.

Registration is per sandbox, not process-wide, so two sandboxes in one program
can offer different commands. It fails at `New` — rather than silently later —
when a command is nil, its name is not a plain word
(`[A-Za-z0-9][A-Za-z0-9_.-]*`), its description is empty, or the name is already
taken by a builtin, by another registered command, or by something the shell
handles itself (`cd`, `time`, `if`, …). Each of those could never be reached: a
script starting with `if` does not parse, and `time` would measure what follows
it rather than run the registration.

`Sandbox.Commands()` returns the registered commands sorted by name, for a host
that wants to render its own listing; sbsh has no `help` command of its own.

[examples/customcommand](examples/customcommand/main.go) is a complete program: a
`wordfreq` command reading files and standard input, registered and then used in
a script alongside the builtins. Dynamic loading and config-file registration are
not supported — a command added this way is Go code compiled into the host.

### Nested execution

A command does not have to reimplement in Go what the sandbox can already do. It
runs a script back inside the sandbox it is running in, the way `bash` runs
`bash`:

```go
res, err := inv.RunNested(ctx, command.NestedRequest{
	Script: `set -o pipefail; cut -d, -f"$FIELD" -- "$FILE" | sort | uniq -c | sort -rn`,
	Env:    []string{"FIELD=2", "FILE=" + inv.Args[0]},
})
if err != nil {
	return command.Exitf(1, "%v", err) // a bad request, or the sandbox itself
}
if !res.OK() {
	return command.Exitf(1, "counting failed: %s", res.Outcome)
}
```

The child runs on the same capability boundary as the caller: the same mounts and
deny patterns, the same network policy, the same registered commands. There is no
route to the host in it, and a `NestedRequest` can only narrow what the child is
given, never widen it.

It is *not* `Sandbox.Exec`. A sandbox is one shell session, and the command is
running inside the execution that holds it, so calling `Exec` from inside would
wait on the very run it is part of. That call is answered with
`OutcomeUnsupported` instead of deadlocking. A nested run gets a shell session of
its own, started where the caller stands and discarded when it ends.

| What | Inherited by the child |
|---|---|
| Filesystem, deny patterns, network policy, commands, Python | Yes — the sandbox's, shared |
| Working directory | The caller's `inv.Dir`, unless `Dir` names another; a relative `Dir` resolves against the caller's |
| `HOME` | Yes — a script has no other way to find it |
| `PWD` | Set by the sandbox from where the child actually runs |
| The rest of the caller's environment | No — name what the script reads in `Env` |
| Shell variables, functions, `set` options | No — a fresh session |
| `OLDPWD` | No — the child has not been anywhere, so `cd -` cannot land in the caller's history |
| Standard input | No — a request is a script, not a filter; the child reads an empty stdin |
| Shell state the child leaves behind | No — its `cd`, its variables and its functions end with it. Files it writes are of course shared |

`Env` entries are `NAME=value` pairs, and they are the whole environment the
child's script reads. A child does not inherit the caller's environment because a
command's environment is a record of everything that has happened to the shell —
a variable some earlier command exported, a value the host passed in for another
command's sake — and a script that reads it behaves differently depending on who
called it. Naming what the script needs makes the request say what it depends on.
An entry naming `PWD` or `OLDPWD` is dropped; those are the sandbox's to set.

Prefer a variable to string concatenation, as above: a value interpolated into
the script is shell source, so a file named `; rm -rf /` would be a second
command. A variable is data whatever it holds.

`Timeout` and `OutputLimit` only ever tighten. The caller's deadline stays in
force and the earlier of the two stops the run; a limit above the sandbox's own
is the sandbox's. Output past the limit is discarded and `res.Truncated` says so,
independently of the outcome — a truncated run still completed.

The result is the same `exec.Result` a host gets from `Exec`, so a caller cannot
tell a child's ending from a top-level one by its shape. Read `res.Outcome`
rather than parsing `res.Stderr`: `OutcomeCompleted` with a non-zero status is
the script's own failure, `OutcomeDenied` a request the sandbox refused,
`OutcomeTimedOut` or `OutcomeCanceled` a limit, `OutcomeUnsupported` a sandbox
with no nested execution at all. The `error` is non-nil only for a request that
makes no sense — an empty script, a working directory that is not there — and for
a failure of the sandbox itself; a child that exits non-zero is not an error, any
more than it is for the host.

Nesting is bounded at 8 levels; past that a request reports `OutcomeDenied`
rather than running. Composing a handful of commands is what this is for, and a
command that invokes itself is a script away.

What v1 leaves out, deliberately:

- **Streaming.** The child's output is captured and returned when it ends;
  nothing is written to `inv.Stdout` as it goes.
- **Piping into a child.** There is no stdin to give it, and no way to run a
  child as a stage of the caller's pipeline. Pass data through a file or through
  `Env`.
- **Loosening anything.** No request can add a mount, a network destination, more
  time, or more output than the caller already has.
- **Re-entering `Sandbox.Exec`.** It is the host's entry point; from inside it
  reports `OutcomeUnsupported` and names the execution already running.

[examples/nestedcommand](examples/nestedcommand/main.go) is a complete program: a
`tally` command that counts a CSV column by composing `cut`, `sort` and `uniq`
inside the sandbox instead of in Go.

## Policies

### Filesystem

Mounts decide what exists and whether it is writable. Deny patterns layer on top
and close read and write together — the case a read-only mount cannot express,
such as `.env` inside an otherwise writable project.

```sh
sbsh --mount ./project:/work \
     --mount ~/.cache/pip:/cache:ro \
     --deny-path '**/.env' \
     --deny-path '/work/secrets'
```

Pattern syntax is deliberately small:

- `*` matches within a single path segment and never matches `/`.
- `**` matches zero or more whole segments and must stand alone as a segment.
- A pattern that does not start with `/` is anchored at any depth: `.env` means
  `**/.env`. A pattern starting with `/` is anchored at the root.
- `?`, `[...]`, and `\` are rejected at parse time rather than silently ignored.

### Network

Without `--allow-net` / `WithNetworkAllow` the sandbox has no network at all. No
HTTP client is constructed, and `curl` exits `1` with
`network access is not permitted`.

An allow-list entry is a host name (`example.com`), a leading-wildcard host name
(`*.github.com` — subdomains at any depth, but *not* `github.com` itself), an IP
address (`192.168.1.1`), or a CIDR block (`10.0.1.0/24`).

A connection is allowed when either check passes:

1. **The name** matches a host entry, when the request is made.
2. **The address** matches an address entry, when the connection is opened — the
   dialer resolves the name itself, checks every address it got back, and
   connects to that exact address. A name allowed this way cannot be pointed
   somewhere else between the check and the connection.

The second check is what lets `--allow-net 10.0.1.0/24` on its own permit
`curl https://example.com` when that name lands inside the block. Redirects are
covered for free: each hop opens a new connection and so goes through the same
check.

## Available commands

None of these is a host binary. Each is a Go function handed the sandbox
filesystem. A host can add commands of its own in Go — see
[Custom commands](#custom-commands).

The shell language is [`mrtc0/sh`](https://github.com/mrtc0/sh/tree/sbsh), a
patched fork of [`mvdan.cc/sh`](https://github.com/mvdan/sh): pipelines,
redirections, heredocs, globs, variables, functions, and `source`.

Three commands come from an existing implementation:

- `python`, `python3` — CPython 3.14 on wazero
- `awk` — [goawk](https://github.com/benhoyt/goawk)
- `jq` — [gojq](https://github.com/itchyny/gojq)

The rest are written in this repository on top of the Go standard library, each
covering a subset of the flags its original accepts:

`curl`, `sed`, `grep`, `diff`, `patch`, `tar`, `gzip`, `gunzip`, `zcat`,
`base64`, `md5sum`, `sha1sum`, `sha256sum`, `cat`, `head`, `tail`, `wc`, `cut`,
`sort`, `uniq`, `tee`, `seq`, `ls`, `find`, `cp`, `mv`, `rm`, `mkdir`, `touch`,
`basename`, `dirname`

`sed` and `grep` use Go's `regexp` package, so patterns are RE2: backreferences
and lookahead are unavailable.

An unrecognized command prints `command not found` and exits `127`; it is never
looked up on the host. A script that ends there reports `OutcomeNotFound`, while
one that handles it and picks its own status reports that status. An unsupported
flag is an error rather than a silent no-op.

`tar` and `patch` treat archive and patch member names as untrusted: a leading
`/` is dropped so the member lands under the extraction root, and a name that
climbs out with `..` is rejected.

### Python

`pip` is not available, and the built-in `site-packages` starts empty. Only
pure-Python code and the compiled-in extension modules (including zlib) are
importable.

Third-party pure-Python packages come from the host, as a directory it prepares
and the sandbox imports from. `sbsh` never installs anything: preparation happens
outside, with whatever tool the host already uses.

```console
$ python -m pip install --target ./vendor/python attrs
$ sbsh --mount ./vendor/python:/lib/python/site-packages:ro -c "python -c 'import attrs; print(attrs.__version__)'"
```

`/lib/python/site-packages` is a convention, not an option: a directory mounted
there is treated as an ordinary site directory, and one that is not mounted costs
nothing. There is no flag to go with it, and the Go API uses the same convention
— `python.LibraryRoot`. `.pth` files in a staged tree are processed the way
Python processes them anywhere else. The root is scoped to the sandbox it is
mounted into.

Dependencies are the host's business: `pip install --target` stages the whole
dependency tree, and whatever it stages is what the sandbox can import; `sbsh`
resolves nothing. There is no validation pass either — whether an import works is
settled at import time by what the sandbox filesystem shows then, so a tree that
a deny pattern hides, or one the host rewrites afterwards, simply fails to
import.

**Pure-Python only.** Compiled extension modules were built for a host ABI and
this interpreter cannot load them, so a package that ships one installs cleanly
with `pip` and then fails to import here, as an ordinary Python import error.
How it reads depends on how the package reaches for its extension; a compiled
file sitting next to the module that could not be imported is the thing to look
for.

## Limits

Per `Exec` call:

- **Timeout** — 30 seconds by default (`WithTimeout`). A script stopped by it
  reports `128 + SIGKILL` (`137`), and one stopped because the caller cancelled
  the context reports `128 + SIGINT` (`130`), the way a real shell reports a
  killed process. Both arrive as `Result.ExitCode` with a nil error, and as
  `OutcomeTimedOut` / `OutcomeCanceled`, so the caller can tell "stopped" from
  "failed on its own" without treating a limit it asked for as a sandbox
  failure. A command that notices the interruption and returns a status of its
  own does not change that: being stopped is why the run ended, so it is what
  the outcome says.
- **Output** — stdout and stderr are captured in memory and capped at 4 MiB each
  by default (`WithOutputLimit`, or `--output-limit` on the CLI). Past the cap,
  output is discarded and `Result.Truncated` is set; the REPL says so on stderr.
  Truncation is never reported as success.
- **Serialization** — `Exec` holds a lock, so concurrent callers queue. A sandbox
  is one shell session, not a pool. A command running inside the sandbox does not
  queue behind it and does not re-enter it: see
  [Nested execution](#nested-execution).
- **Nesting depth** — executions may nest 8 levels deep. A request past that
  reports `OutcomeDenied` rather than running.
- **No bytecode cache** — every `python` invocation compiles the modules it imports
  from source. The standard library is mounted read-only and no `.pyc` is shipped,
  because a committed one could never be accepted: its recorded source timestamp
  cannot match a tree that is copied into memory at startup.

Not implemented:

- **CPU and memory limits.** A busy loop is stopped by the timeout, not by a
  resource cap. Memory is bounded only by the Wasm runtime's own limits.
- **Process substitution** (`<(...)`), rejected at parse time because it needs
  host FIFOs. Command substitution, pipelines, and redirections all work.
- **`pip` and package installation.** The host stages a package tree and the
  sandbox imports from it — see [Python](#python).
- **Packages with compiled extensions**, for the same reason.
- **Persistent history** across REPL sessions.
- **Interrupting a running script with Ctrl-C at an interactive terminal.** Raw
  mode clears `ISIG`, so the keystroke never becomes a signal, and the REPL is not
  reading input while a script runs. The keystroke is read once the script
  finishes, where it cancels the (empty) prompt rather than the script. Sending
  `SIGINT` from another terminal interrupts the script as described above, and
  piped input and `-c` are unaffected.
- **Windows support** is untested; `HostFS` relies on `os.Root` semantics.

## Security model

**What sbsh is for.** Running a script an agent wrote, without reading it first.
The script reaches the directories you mounted and the network destinations you
allowed, and nothing else on the host.

**Trust boundary.** Sandboxed code is untrusted; the host process is trusted.
There are exactly two crossings, both closed by default:

1. **Filesystem** — only through `vfs.HostFS`, which delegates to `os.Root`.
   Symlinks inside a mount cannot escape it, `..` is normalized away, and
   read-only mounts return `EROFS`. A path no mount covers does not exist as far
   as the sandbox is concerned. Deny patterns are matched against resolved paths,
   so a link inside the mount is not a way past them.
2. **Network** — only through the `http.Client` that `netpolicy` builds, with the
   allow-list check described above.

Everything else is absent rather than filtered: no host process is ever created,
and the Wasm module receives a filesystem and no other capability.

### Out of scope

- **Preventing exfiltration of what the sandbox may legitimately read.** If a
  mount exposes a file and the allow list permits a destination, sbsh will not
  stop one from reaching the other. Scope the mounts and the allow list.
- **Where an allowed name points.** A host entry grants whatever that name
  resolves to. `--allow-net example.com` reaches `127.0.0.1` or
  `169.254.169.254` if that is what `example.com` answers, so a name whose DNS
  you do not control is a possible route to a service on your machine or to the
  cloud metadata endpoint. List names you trust to resolve where you expect, and
  use an IP or CIDR entry when the destination has to be pinned.
- **Resource exhaustion.** The timeout bounds how long a script runs; the output
  cap bounds how much output is kept. Memory is not bounded — the Wasm runtime is
  created without a memory limit, and builtins such as `sort` hold their input on
  the host process's heap — so a large enough input takes the host process down
  with the sandbox.
- **Wasm-runtime and Go-stdlib vulnerabilities.** sbsh is a defense-in-depth
  layer inside one process, not an OS- or VM-level sandbox. For genuinely hostile
  input, rather than merely untrusted generated code, run sbsh inside a real
  isolation boundary as well.
- **Read-only versus write-only deny rules.** Deny patterns close both
  directions; the read/write distinction belongs to mounts.
- **Aliases a path cannot show.** Deny patterns select names, and symlinks are
  resolved before matching, but a hard link or a bind mount in the mount source
  reaches the same file under a name that looks unrelated. A file that must not be
  read belongs outside the mount.

## License

MIT. See [LICENSE](LICENSE).

The shell interpreter is [`github.com/mrtc0/sh`](https://github.com/mrtc0/sh/tree/sbsh),
a fork of [mvdan.cc/sh](https://github.com/mvdan/sh) v3.13.1 licensed under
BSD-3-Clause; see [its LICENSE](https://github.com/mrtc0/sh/blob/sbsh/LICENSE).

An `sbsh` binary embeds CPython, so distributing it distributes CPython. That is
done under the [PSF License](https://docs.python.org/3/license.html), which asks
that the License Agreement and PSF's copyright notice travel with the copy. The
same applies to what `python.wasm` is linked against: wasi-libc (Apache-2.0 WITH
LLVM-exception, Apache-2.0 and MIT), LLVM's compiler-rt, and zlib.

Those texts live in [pywasm/dist/licenses/](pywasm/dist/licenses/), collected by
[pywasm/Dockerfile](pywasm/Dockerfile) from the same sources the artifact is built
from, and recorded in [pywasm/dist/PROVENANCE](pywasm/dist/PROVENANCE) by version.
Release archives carry them under `licenses/`.

[THIRD_PARTY_LICENSES](THIRD_PARTY_LICENSES) collects everything in one file — the
Go modules linked into `cmd/sbsh` and the embedded Python runtime — and is
regenerated by
[scripts/gen-third-party-licenses.sh](scripts/gen-third-party-licenses.sh).
