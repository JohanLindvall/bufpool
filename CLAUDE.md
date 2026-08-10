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
  at message boundaries. A `Release`/`Get` round-trip does *not* bound it:
  `Get` re-slices the same array to `[:0]`, so it resets `Size` but not `Cap`,
  and the grown array is inherited by whichever unrelated caller gets it next.
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
  (`Grow`, reallocation in the write paths via `beforeAppend`, `Reset`'s
  discard branch) must call `abandon()` — it clears the strike counter *and*
  the pooled storage's stale slice header so the old array is not pinned until
  the next `Release`. `SetBytes` is the one exception (changed 2026-08-10,
  restoring 0.1.0's intent with a correct same-array test): it clears the
  header unconditionally (`dropStorageRef`) but resets strikes only when the
  adopted slice is a foreign array (`sameArray`, terminal-address comparison).
  In-place adoption of the buffer's own array — Fill's no-allocation path —
  must preserve strike history, or a Fill cycle resets the counter every
  round trip and an oversized under-utilized array is never evicted
  (`Test_unit_Fill_DoesNotDefeatEviction`). Keep the header clear
  unconditional: gating it on the same test re-pins the old array when a
  smaller foreign array is adopted.
- `keep()` must run before the length is truncated; utilization is measured
  on the written length.
- Oversized-allocation panics are unified as the exported `ErrTooLarge` (via
  `makeBuf`); write-path reallocation is routed through `Grow` for this reason.
  It must stay an `error` value, not a string — that is the whole point of
  exporting it, and `buf_test.go` asserts `recover()` yields something
  `errors.Is`-comparable. The programmer-error panics (negative counts,
  contract-violating `io.Writer`/`io.Reader`) stay plain strings, as in `bytes`.
- `Next` and `ReadAllBytes` return three-index slices capped to their length,
  so appending to one allocates rather than writing into the buffer. `Bytes`
  is deliberately *not* capped (decided 2026-08-10, reverting the 0.2.4
  capping): like `bytes.Buffer.Bytes`, its capacity runs to the end of the
  backing array so downstream code can hand the pool's spare capacity to an
  appending encoder and adopt the result. The trade-off is documented, not
  fixed in code: the `Bytes` doc comment, README ("Ownership and aliasing")
  and COMPARISON.md §4 all warn that the slice is a writable window onto the
  buffer and must not be used to modify it outside that idiom — keep those
  warnings in sync if `Bytes` is touched, and do not re-cap it. Anything
  inside the repo that wants the array's real capacity must still use `Cap()`,
  not `cap(b.Bytes())` — the latter is offset by the read position.
- `Scratch` (added 2026-08-10) returns `buf[len:cap]` at *full length*, not
  length 0: decode-into-dst APIs (`snappy.Decode` and kin) test `len(dst)`,
  not `cap(dst)`, so an `AvailableBuffer`-style empty slice would silently
  never reuse pooled capacity. `Fill` wraps scratch → decode → adopt around a
  caller closure; the closure shape was chosen over an `Adopt(p, err)`
  passthrough (considered same day) precisely so no variable aliasing the
  scratch or adopted slice can survive in caller scope. Fill must stay a
  trivial three-liner — no Release-on-error or other hidden control flow —
  and must adopt fn's result even on error.
- `makeBuf` allocates with `append([]byte(nil), make([]byte, capacity)...)`,
  not `make([]byte, length, capacity)`. This is deliberate and must not be
  "simplified": `make` reports the requested capacity while the allocator has
  already reserved `roundupsize(capacity)`, so the exact-fit form throws the
  size-class slack away and the next byte past the requested size forces a full
  doubling plus a memmove — `Write(1500)` then one more byte settled at `Cap`
  3000 where `bytes.Buffer` holds at 1536. `bytes.growSlice` uses the same
  pattern for the same reason.

## Verification

- Tests must keep 100% statement coverage; run `go test -race -shuffle=on -cover ./...`.
- CI gates on `golangci-lint` as a separate job, so `gofmt`/`go vet`/`go test`
  passing locally is not enough — run `golangci-lint run ./...` too (it lives in
  `~/go/bin`, which may not be on `PATH`). A green local suite with an unrun
  linter is how v0.2.4 was missed: an ineffectual assignment in a new test
  turned main red and skipped the release.
- The comparison harness and differential fuzzer live in `_bench/` (its own
  module); run the fuzzer after changing Buffer semantics:
  `cd _bench && go test -fuzz FuzzDifferential -fuzztime 30s .`
- CI auto-tags every green main commit as the next patch version — pushing to
  main is releasing.
