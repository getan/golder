# Golder's retained input-decoder patch

This is an unmodified module snapshot of `github.com/charmbracelet/ultraviolet`
at `v0.0.0-20260812204455-68fa937c71be` (MIT), with the narrowly scoped input
changes listed below. The upstream LICENSE, sources and tests are retained.
The root `go.mod` replaces the module so the workspace client, ordinary agent
TUI, and embedded browser all use **the same** corrected decoder. There is no
second stdin reader and no text-pattern filter in a composer or PTY daemon.

Local changes:

- `input_frame.go`: frame CSI/SS3/X10/OSC/DCS/APC/PM/SOS before event decoding;
  keep incomplete frames across reads and the ordinary Esc timeout; bound
  oversized reports and recover at terminators, new ESC, or interrupt keys.
- `terminal_reader.go`: apply framing, preserve opaque bracketed paste and
  incomplete UTF-8, stop idle retry timers for protocol continuations, deliver
  bytes returned alongside EOF, and respect cancellation while sending events.
  Win32 normalization now runs per complete record and counts source bytes,
  rather than mixing normalized lengths into the original input offsets.
- `decoder.go`: accept the shorter eight-bit CSI X10 mouse prefix rather than
  indexing it as a six-byte seven-bit report (a fuzz-reproduced panic).
- Regression tests exercise all split positions, delayed reads, malformed and
  oversized reports, literal mouse-looking text, paste, and real workspace input.

A bare Esc keeps the upstream 50ms behavior. The seven-bit two-byte introducers
also encode Alt+[ / Alt+] / Alt+O etc.; they get a separate one-second ambiguity
window. Once a payload byte identifies a report it is retained until completion
or explicit recovery, rather than reinterpreted as typing after a timer.
Report bodies are bounded at 4KiB (CSI/SS3) or 4MiB (control strings), bracketed
pastes at 16MiB. An oversized frame/paste is discarded as a whole, never emitted
as truncated input. A missing string terminator may require Esc or Ctrl+C to
recover: without a protocol boundary, distinguishing payload from typing is not
possible without risking user-data loss.

Run from the repository root (also uses the root dependency versions):

```sh
GOWORK=off go test github.com/charmbracelet/ultraviolet/...
GOWORK=off go test ./internal/workspace ./internal/cli/tui
```

When upstream provides equivalent framing, remove the root replacement and this
snapshot only after the split-input and workspace regressions pass against the
new version. Do not edit unrelated rendering/layout code when updating it.
