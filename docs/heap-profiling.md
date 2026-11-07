# On-demand Go heap profiles

This diagnostic is opt-in and supported on macOS and Linux. It exposes no
network listener. A snapshot forces a garbage collection, which can briefly
interrupt the process. Profiles contain function names, source paths and memory
statistics; treat them as sensitive diagnostic files and do not publish them
without review. They do not contain a raw dump of conversation text.

Build a diagnostic binary and create a private output directory, then start the
**agent process** with the environment variable set (use its usual session ID):

```sh
DIAG_DIR=$(mktemp -d "${TMPDIR:-/tmp}/golder-heap.XXXXXXXX")
go build -o "$DIAG_DIR/golder" ./cmd/golder
printf 'Diagnostic directory: %s\n' "$DIAG_DIR"
GOLDER_HEAP_PROFILE_DIR="$DIAG_DIR" "$DIAG_DIR/golder" --resume SESSION_ID
```

The directory must already exist and be writable by that process. Starting a
workspace client with the variable does not change an already-running daemon
or agent; set it in the agent's environment when that agent starts. An inherited
variable can also enable profiling in other newly started golder processes, so
identify and signal the agent PID, not its workspace daemon or a client. The
`workspace`, `browse` and other long-lived golder modes can be profiled the
same way if explicitly started with the variable.

**Do not send SIGUSR2 to an old binary, an unconfigured process, or one whose
startup reported `heap profiling disabled`: the signal may terminate it.** The
variable must have been set at process startup, not merely in the shell sending
the signal. Confirm the PID and startup configuration before each signal:

```sh
kill -USR2 AGENT_PID
ls -l /path/to/diagnostic-directory/golder-heap-AGENT_PID-*.pprof
```

Each signal produces a uniquely named, mode-0600 file. Wait for the `.pprof`
file before sending another signal; `.partial` files are not completed
snapshots. Failures are reported on the process's stderr. Take one baseline
after the resumed session is idle and another after memory has grown:

```sh
go tool pprof -top -inuse_space /path/to/later.pprof
go tool pprof -top -inuse_space -base /path/to/baseline.pprof /path/to/later.pprof
```

The second command shows the difference in sampled live Go heap by allocation
stack; negative values indicate memory released since the baseline. Profiles
are sampled and do not account for every part of RSS or macOS physical
footprint. Compare OS process metrics separately. Restarting discards the old
process's state: a resumed session can measure history reload, but it does not
necessarily reproduce the original long-running process. Remove the profile
files and diagnostic binary when finished.
