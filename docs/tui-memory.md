# Agent TUI memory

The agent, workspace client, workspace daemon and browser host are separate
process roles even though their executable is named `golder`. Check the role
before attributing an RSS increase to the workspace or to Chromium. RSS / macOS
physical footprint is not the same measurement as Go's live heap: it also
includes runtime headroom, stacks and executable/library pages.

## Render retention fixes

The transcript retains the original conversation/tool results so history can
be reopened. Derived render data must not retain more history than that:

- Collapsing an expanded card or rebuilding at a different width can shrink
  `prefixLines` and viewport `lines` dramatically. Reslicing alone leaves old
  strings in the backing array. Clear discarded entries and release oversized
  pointer arrays after a large shrink.
- A cache bounded only by entry count is not bounded in bytes. Diff rendering
  now has a 4 MiB text budget and a 1 MiB per-entry admission limit; larger
  diffs still render fully but are not retained by that global cache.
- Syntax-highlight results have a 2 MiB accounting budget, including keys and
  a conservative span allowance. Tokens must refer to cache-owned text, not a
  short substring that pins a whole large tool result.
- Lexer filename lookup is limited to 1,024 ordinary-sized basenames, and the
  Markdown renderer cache keeps at most four widths across pane/sidebar
  resizes. Those are render caches, not limits on conversation content.

No message/tool output truncation, forced-GC loop or global Go heap limit is
introduced by these fixes.

## Reproduce

```sh
GOWORK=off GOMAXPROCS=4 go test ./internal/cli/tui \
  -run '^$' -bench 'Benchmark(CollapsedToolRetainedHeap|DiffCacheRetainedBytes)$' \
  -benchtime=1x -count=3
```

On darwin/arm64 (Apple M4), the same synthetic fixtures measured:

| Fixture | Before | After |
| --- | ---: | ---: |
| Live heap retained after expanding/collapsing a roughly 3 MB tool result | 10,133,360 B | approximately 3,550,000 B |
| Cached render strings after rendering 64 large diffs | 18,822,976 B | 2,352,872 B |

The first measurement collects before/after while keeping the transcript
alive. The second counts retained cache key/value bytes. These are scoped
reproduction measurements, **not** a before/after RSS measurement of an
already running agent. Long conversations still legitimately need memory for
model context and raw history. Restart into the new binary to apply the fixes;
old processes cannot replace their rendering code in place.
