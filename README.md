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
  -v, --version               Print the version
```

With no `-c`, `sbsh` starts a REPL. On a terminal it provides line editing and
in-session history; when stdin is a pipe it reads line by line, so an agent can
feed it a script directly. `-c` runs one script and exits with the script's
status.

`SIGINT` stops the running script and leaves the REPL at its prompt; `-c` exits
`130`. `SIGTERM` stops the script and the process, which exits `143`. Either way
the sandbox is closed on the way out. Pressing Ctrl-C at a terminal is a separate
matter — see Not implemented below.

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

`Exec` separates the two kinds of failure. A script that exits non-zero is a
successful `Exec` with a non-zero `Result.ExitCode`; the returned `error` is
reserved for the sandbox itself failing — a parse error, or syntax the sandbox
refuses to run.

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
filesystem.

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
looked up on the host. An unsupported flag is an error rather than a silent
no-op.

`tar` and `patch` treat archive and patch member names as untrusted: a leading
`/` is dropped so the member lands under the extraction root, and a name that
climbs out with `..` is rejected.

### Python

`pip` is not available yet, and `site-packages` starts empty. Only pure-Python
code and the compiled-in extension modules (including zlib) are importable.

## Limits

Per `Exec` call:

- **Timeout** — 30 seconds by default (`WithTimeout`). A script stopped by it
  reports `128 + SIGKILL` (`137`), and one stopped because the caller cancelled
  the context reports `128 + SIGINT` (`130`), the way a real shell reports a
  killed process. Both arrive as `Result.ExitCode` with a nil error, so the caller
  can tell "stopped" from "failed on its own" without treating a limit it asked
  for as a sandbox failure.
- **Output** — stdout and stderr are captured in memory and capped at 4 MiB each
  by default (`WithOutputLimit`). Past the cap, output is discarded and
  `Result.Truncated` is set; the REPL prints `(output truncated)`. Truncation is
  never reported as success.
- **Serialization** — `Exec` holds a lock, so concurrent callers queue. A sandbox
  is one shell session, not a pool.
- **No bytecode cache** — every `python` invocation compiles the modules it imports
  from source. The standard library is mounted read-only and no `.pyc` is shipped,
  because a committed one could never be accepted: its recorded source timestamp
  cannot match a tree that is copied into memory at startup.

Neither the timeout nor the output limit is exposed as a CLI flag today; they are
Go API options.

Not implemented:

- **CPU and memory limits.** A busy loop is stopped by the timeout, not by a
  resource cap. Memory is bounded only by the Wasm runtime's own limits.
- **Process substitution** (`<(...)`), rejected at parse time because it needs
  host FIFOs. Command substitution, pipelines, and redirections all work.
- **`pip` and package installation.**
- **Persistent history** across REPL sessions.
- **Ctrl-C at an interactive terminal.** Raw mode clears `ISIG`, so the keystroke
  never becomes a signal; the line editor reports it as end of input and the REPL
  exits, the same as Ctrl-D. A Ctrl-C pressed while a script runs is read once the
  script finishes, and ends the session then. Sending `SIGINT` from another
  terminal interrupts the script as described above, and piped input and `-c` are
  unaffected.
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
