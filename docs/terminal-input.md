# Terminal input framing

The workspace client, ordinary golder TUI and embedded browser share Bubble
Tea's Ultraviolet terminal reader. A retained, MIT-licensed snapshot under
`third_party/ultraviolet` supplies the same fix to all three via the root module
replacement. Provenance and removal criteria: [GOLDER_PATCHES.md](../third_party/ultraviolet/GOLDER_PATCHES.md).

## Why characters such as `65;59M` appeared in the composer

`ESC [ < 0 ; 65 ; 59 M` is an SGR mouse report. Previously, if a read ended at
`ESC[<0;` and its continuation arrived after the 50ms Esc timeout, the decoder
consumed the unfinished prefix as unknown input and decoded `65;59M` as keys.
The workspace forwarded those keys to the pane normally. This reproduced even
with a plain shell `read` instead of golder in the pane, and also with a real
inner golder composer. Disabling logo animations does not repair input framing.

The same issue affected incomplete OSC color/clipboard replies, CSI query
replies, focus reports, keyboard sequences, and partial X10 mouse payloads or
string terminators. The fix retains **protocol frames**, not text patterns.
Typing or pasting the literal text `65;59M` still works.

## Behavior and limits

- A bare Esc keeps its short timeout; two-byte introducers that could also be
  Alt keys get a separate one-second ambiguity window. Once payload identifies
  a protocol frame, its continuation is not flushed by a key timeout.
- SGR/X10 mouse, CSI/SS3 keys and query responses, OSC, DCS, APC, PM and SOS are
  framed before semantic decoding, including split `ESC \\` terminators.
- UTF-8 characters wait for their remaining bytes. Bracketed-paste content is
  opaque data, including text that resembles terminal reports; split paste end
  markers do not appear as typing.
- Reports are bounded at 4KiB for CSI/SS3 and 4MiB for control strings, pastes
  at 16MiB. Oversized frames/pastes are discarded whole rather than truncated
  into input. The reader can resynchronize at a new escape sequence or an
  interrupt key. There is no second reader competing for stdin.
- Bytes already missing their prefix cannot safely be distinguished from
  intentionally typed text. No regex removes report-looking numbers or color
  strings from a draft. A stalled, unterminated control string may need Esc or
  Ctrl+C to recover. Very late bytes after an ambiguous bare Esc/Alt key has
  expired remain intrinsically ambiguous.

## Loading the fix

An already-running process retains its old decoder. After updating the binary,
detach and attach the workspace client again; restart/resume an existing inner
golder process when it also needs the new decoder. The daemon need not be
stopped merely to replace the client's input decoder.

## Regression checks

```sh
GOWORK=off go test -race ./... github.com/charmbracelet/ultraviolet/...
GOWORK=off go -C third_party/ultraviolet test -run '^$' \
  -fuzz FuzzInputFrameChunks -fuzztime 10s -parallel 1
```

The workspace tests use isolated real PTYs: one checks what a plain shell reads,
the other captures the actual inner golder composer after delayed mouse, color,
cursor, focus, device-attribute and graphics reports. No model call is made.

## Cursor consistency during synchronized redraws

The workspace compositor pairs a cursor snapshot (position, visibility, shape,
blink and color) with the terminal body it sampled under the pane I/O lock.
While DEC 2026 is open, and on cached/skipped Views, both body and cursor stay
on the last displayed frame. With no complete frame yet the cursor is hidden;
the cell loop must not replace it with a fake mid-body cursor. The existing
bounded sync timeout still releases a guest that never closes its transaction.

Golder's retained Ultraviolet renderer also hides the cursor while painting,
even inside a synchronized transaction. Only the final visible View cursor is
shown again, after its final position is emitted and before 2026l. This is a
defense for older multiplexers, not a replacement for compositor consistency.
The CJK/wide-glyph erase-and-repaint path is preserved. Restart the inner agent
to load the renderer defense; detach/reattach a new workspace client to load
the compositor fix. Updating a binary does not replace already-running code.
