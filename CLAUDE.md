# bufpool — notes for Claude

## Design decisions

### No automatic compaction of the consumed prefix (deliberate)

Reads never reclaim memory and writes always append after the consumed
prefix, even when the buffer is fully drained. An auto-compaction variant
(compact-on-write-to-drained-buffer, like `bytes.Buffer`'s reset-on-empty)
was implemented and then deliberately reverted: it silently weakened `Rewind`
and `Size`, whose contract is that the *full* written contents stay
replayable until an explicit `Reset`/`Release`.

Consequences, documented in README ("Streaming") and doc.go:

- A buffer used as a long-lived FIFO grows with the total bytes streamed
  through it, not the working set. The supported way to bound it is `Reset`
  at message boundaries or a `Release`/`Get` round-trip.
- Do not "fix" this by adding compaction to the write paths (`Write`,
  `WriteString`, `WriteByte`, `ReadFrom`); that trades away the Rewind/Size
  contract. If bounded FIFO use ever becomes a requirement, add an explicit
  opt-in method instead.

The differential fuzzer (`_bench/fuzz_test.go`) cannot detect this class of
change either way: it compares contents and lengths against `bytes.Buffer`,
and only capacity/retention behavior diverges.

## Invariants worth knowing

- A pool-attached Buffer always carries a non-nil `storage`; `Release` copies
  the handle state back into it. Any path that abandons a backing array
  (`SetBytes`, `Grow`, reallocation in the write paths via `beforeAppend`,
  `Reset`'s discard branch) must call `abandon()` — it clears the strike
  counter *and* the pooled storage's stale slice header so the old array is
  not pinned until the next `Release`.
- `keep()` must run before the length is truncated; utilization is measured
  on the written length.
- Oversized-allocation panics are unified as `"bufpool.Buffer: too large"`
  (via `makeBuf`); write-path reallocation is routed through `Grow` for this
  reason.

## Verification

- Tests must keep 100% statement coverage; run `go test -race -shuffle=on -cover ./...`.
- The comparison harness and differential fuzzer live in `_bench/` (its own
  module); run the fuzzer after changing Buffer semantics:
  `cd _bench && go test -fuzz FuzzDifferential -fuzztime 30s .`
- CI auto-tags every green main commit as the next patch version — pushing to
  main is releasing.
