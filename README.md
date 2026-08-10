# bufpool

A small Go package for pooling and reusing byte buffers, reducing allocations
and garbage-collector pressure in code that handles many short-lived buffers.

A `Buffer` implements `io.Reader`, `io.ByteReader`, `io.Writer`,
`io.ByteWriter`, `io.StringWriter`, `io.ReaderFrom`, `io.WriterTo`,
`io.Closer` and `fmt.Stringer`, so it drops into most code that already
speaks the standard streaming interfaces. Buffers
obtained from a `Pool` are returned to it for reuse, and an adaptive *strike*
heuristic discards backing arrays that have grown large but are repeatedly
under-utilized, so a single large write does not pin memory indefinitely. See
[COMPARISON.md](COMPARISON.md) for how this stacks up against `sync.Pool`
idioms and other buffer pools.

Adapted from <https://github.com/golang/go/issues/27735#issuecomment-739169121>.

## Install

```sh
go get github.com/JohanLindvall/bufpool
```

```go
import "github.com/JohanLindvall/bufpool"
```

## Quick start

```go
var pool bufpool.Pool // the zero value is ready to use

// Get a buffer from the pool.
buf := pool.Get()

buf.WriteString("hello ")
buf.Write([]byte("world"))

// Use it as an io.Reader.
data, _ := bufpool.ReadAllBytes(buf) // []byte("hello world"), zero-copy for *Buffer
process(data)                        // use (or copy) the bytes *before* releasing:
                                     // data aliases the buffer's backing array

// Return the buffer to the pool for reuse. This invalidates data;
// do not use buf or data afterwards.
buf.Release()
```

Because a `Buffer` is an `io.Closer`, it also works with `defer`, and can be
handed off as an `io.ReadCloser` that returns itself to the pool when closed:

```go
func payload(pool *bufpool.Pool) io.ReadCloser {
    buf := pool.Get()
    buf.WriteString("body")
    return buf // the consumer's Close returns the buffer to the pool
}
```

The consumer now decides *when* the buffer is released, possibly on another
goroutine, so hold no slice from `Bytes`/`Next`/`ReadAllBytes` across the
handoff. **`net/http` is the case to watch.** Since `*Buffer` already satisfies
`io.ReadCloser`, `http.NewRequest` adopts it as `req.Body` verbatim instead of
wrapping it, and the transport closes — and therefore releases — it before
`Do` returns. Its type switch also special-cases only `*bytes.Buffer`,
`*bytes.Reader` and `*strings.Reader`, so the request goes out with
`ContentLength -1` and `Transfer-Encoding: chunked`, and with a nil `GetBody`
that stops a 307/308 redirect from replaying the body.

These are two separate problems and neither remedy fixes both — `Detach` only
stops the release, and the fields only fix the wire format — so do both:

```go
buf.Detach() // the transport's Close must not return it to the pool
req, _ := http.NewRequest("POST", url, buf)
req.ContentLength = int64(buf.Len())                       // identity, not chunked
req.GetBody = func() (io.ReadCloser, error) {              // replay on 307/308
    buf.Rewind()
    return buf, nil
}
```

Returning `buf` itself from `GetBody` is only safe because `Detach` has already
made its `Close` a no-op. Drop the `GetBody` if you do not need redirects
followed; without one the client returns the 307/308 response rather than
following it.

## Usage

### Pooling

```go
var pool bufpool.Pool

buf := pool.Get()
```

Calling `Release` (or `Close`) returns the buffer to the pool and resets it
to the zero value, so it must not be used afterwards. Re-acquire one with
`Get`. A `Buffer` must not be copied after first use (`go vet` reports such
copies).

### Reading and writing

A `Buffer` separates writes (which append) from reads (which consume from the
front, tracked by an internal read position):

```go
buf := pool.Get()
buf.WriteString("abcdef")

p := make([]byte, 3)
buf.Read(p)            // p = "abc", read position now at 3
buf.Bytes()            // []byte("def") — the unread remainder (aliases the buffer)
buf.Next(2)            // []byte("de") — consume 2 bytes zero-copy (aliases the buffer)
buf.String()           // "f" — a copy, safe to keep after release
buf.Len()              // 1 — unread bytes, like bytes.Buffer.Len
buf.Size()             // 6 — total written length, including consumed bytes
buf.Cap()              // capacity of the backing array

buf.Rewind()           // reset the read position to re-read from the start
```

`ReadByte` and `WriteByte` round out the byte-at-a-time interfaces
(`io.ByteReader`, `io.ByteWriter`), so callers like `binary.ReadUvarint` work
directly on a `Buffer` without a `bufio` wrapper.

`WriteTo` streams the unread portion to any `io.Writer`, and `ReadFrom` fills
the buffer from any `io.Reader`, so `io.Copy` in either direction avoids
intermediate copy buffers:

```go
n, err := io.Copy(buf, resp.Body) // uses buf.ReadFrom, no 32 KiB scratch buffer
```

### Streaming

Reads never reclaim memory: the consumed prefix stays in place — that is what
lets `Rewind` replay the full contents — and writes always append after it.
`Rewind`'s replay is therefore complete: it always reaches back to the last
point the buffer was empty (its creation, or the most recent `Reset` or
`SetBytes`), no matter how reads and writes were interleaved in between.
Used as a long-lived FIFO on a single buffer (write a chunk, read it, repeat),
the buffer therefore grows with the total bytes streamed through it, not with
the working set. Bound it by calling `Reset` at natural message boundaries
(it rewinds, truncates, and applies the same keep-or-discard heuristic as the
pool).

A `Release`/`Get` round-trip does **not** bound it: `Get` re-slices the same
array to `[:0]`, so the round trip resets `Size` but not `Cap`. The growth also
outlives the buffer — a FIFO buffer that grew to N bytes hands an N-byte array
to the pool, where it scores as well utilized and is handed on to unrelated
callers regardless of how little they asked for. `Reset` is what bounds
pool-wide memory, not just one buffer's.

When the output size is known in advance, `Grow` pre-allocates capacity so
subsequent writes do not reallocate:

```go
buf.Grow(len(payload))
buf.Write(payload)
```

Reserved capacity is not exempt from the strike heuristic below, which measures
utilization as written length against capacity. Capacity above 64 KiB that you
reserve but leave unfilled counts as under-utilized, so a buffer that reserves
far more than it writes loses the reservation every fifth cycle. Reserve close
to what you will actually write.

### Ownership and aliasing

The zero-copy calls trade safety for speed; their rules are:

- `Bytes`, `Next`, `ReadAllBytes` and `Scratch` return slices that **alias**
  the buffer. `Release`, `Close` and `Reset` invalidate them — the backing
  array re-enters the pool and the next `Get` may overwrite it. Copy the bytes
  (or use `String`) if they must outlive the buffer.
- The `Next` and `ReadAllBytes` slices are capped to their length (a
  three-index slice), so **appending** to one allocates a fresh array rather
  than writing into the buffer. This diverges from `bytes.Buffer`, where
  appending to a `Next` slice silently overwrites the bytes not yet read.
- The `Bytes` slice is **not** capped: as with `bytes.Buffer.Bytes`, its
  capacity runs to the end of the backing array, so appending to it fills the
  buffer's spare capacity without allocating. This exists for one idiom —
  handing the slack to an appending encoder and adopting the result
  (`p := enc.MarshalAppend(buf.Bytes(), msg)`). It also makes the slice a
  writable window onto the buffer: **do not modify the buffer through it** in
  any other pattern. Appended bytes lie beyond the buffer's length — the
  buffer's own next write lands directly over them, and `Release` hands the
  array, appended bytes included, to an unrelated caller. Finish with (or
  copy) the result before writing to, releasing or resetting the buffer.
- `Scratch` is the destination-slice counterpart of the `Bytes` append idiom:
  it returns the spare capacity as a **full-length** slice, sized for APIs
  that fill a caller-provided `dst` when `len(dst)` suffices and allocate
  otherwise. `Fill` wraps the whole sequence — scratch, decode, adopt — in
  one call, and its closure keeps every aliasing slice out of the caller's
  scope:

  ```go
  buf := pool.Get()
  err := buf.Fill(func(dst []byte) ([]byte, error) {
      return snappy.Decode(dst, packed)
  })
  ```

  Either way the decode goes, the buffer ends up owning the result: in place
  with no allocation when the scratch space sufficed, or adopting the
  decoder's fresh exact-size array — which warms the pool for the next round
  trip. Use it on an empty buffer (adoption discards existing contents), and
  note that on a pooled buffer the scratch space initially holds a previous
  user's bytes (see [Secrets](#secrets)).
- `NewBuffer` and `SetBytes` **adopt** the given slice as the backing array
  without copying. Ownership transfers to the buffer (and, once released, to
  the pool): the caller must not use the slice afterwards. Because the pool
  sizes a buffer by `cap()` alone, adopting a capacity-capped sub-slice of a
  much larger array (`arena[:n:n]`) hands the pool the whole array while the
  heuristic classifies it by the small capacity, and it is never evicted — pass
  a full-capacity slice or `bytes.Clone` it instead.
- Handing a buffer to a consumer that calls `Close` transfers release timing to
  that consumer, possibly on another goroutine, so no aliased slice may be held
  across the call. `net/http` needs particular care — see below.

### Secrets

Nothing on the `Release`/`Get` path clears a backing array, so a released
buffer's bytes stay resident in the pool and are handed to the next, unrelated
caller — readable through that buffer's spare capacity, and passed to any
`io.Reader` that `ReadFrom` gives the spare capacity to. `Wipe` zeroes the whole
array (unread bytes, consumed prefix and spare capacity alike) and then resets
the buffer:

```go
buf := pool.Get()
defer func() { buf.Wipe(); buf.Release() }()
```

Unlike `Reset`, `Wipe` does not apply the keep-or-discard heuristic, so the
following `Release` or `Reset` is still the only application — wiping does not
charge the array twice. It does leave the buffer empty for that application to
score, so an array above 64 KiB takes a strike where releasing it unwiped would
have kept it, and is dropped after five such cycles. That is the intended trade
for not leaving secrets in the pool.

It costs a `memclr` of the full capacity, which is why it is opt-in rather than
part of `Release`. It reaches from the current backing slice's start through its
capacity, so for a slice adopted via `NewBuffer`/`SetBytes` it does not touch
bytes before that start or beyond a capped capacity — and it cannot reach an
array the buffer has already outgrown or otherwise replaced. Wipe before the
buffer grows, not only at the end.

### Detached buffers

A `Buffer` can be used standalone, without a pool, via `NewBuffer`:

```go
buf := bufpool.NewBuffer([]byte("seed")) // adopts the slice; don't reuse it
buf.WriteString(" more")
```

A detached buffer's `Release`/`Close` are no-ops, so `defer buf.Close()` is
safe regardless of a buffer's origin. `Detach` turns a pooled buffer into a
detached one — useful when its contents must outlive a consumer that closes
it.

### In-place reuse

`Reset` rewinds the read position and truncates the buffer for reuse while
keeping it attached to its pool — useful in a tight loop where you want to reuse
the same `Buffer` without round-tripping through `Get`/`Release`:

```go
buf := pool.Get()
for _, item := range items {
    buf.Reset()
    buf.WriteString(item)
    // ... use buf ...
}
buf.Release()
```

## The strike heuristic

To avoid keeping unnecessarily large backing arrays alive, both `Release` and
`Reset` apply the same heuristic when deciding whether to keep a buffer's
backing array:

- Buffers with capacity ≤ 64 KiB are always kept (strike counter cleared).
- Buffers that are at least 50% utilized are always kept (strike counter
  cleared).
- An oversized, under-utilized buffer is given up to four consecutive *strikes*;
  on the fifth it is discarded — the buffer is left with **no backing array at
  all** (`Cap()` returns 0), and the next write allocates one from scratch.

This means a single large usage is not kept alive forever by a continuous stream
of small ones, while transient large usages are still tolerated.

Two details worth knowing. The four-strike budget is spent only by
*consecutive* under-utilized applications, so a large but well-utilized release
recurring more often than once per five small ones clears the counter every time
and keeps its array resident indefinitely. And each `Reset` and each `Release`
counts as one application, so a `Reset` immediately before a `Release` charges
the array two strikes for one use — it is redundant anyway, since `Release`
resets the handle. The [package documentation](https://pkg.go.dev/github.com/JohanLindvall/bufpool)
is the authoritative description of the policy.

## API overview

| Symbol | Description |
| --- | --- |
| `Pool` | Buffer pool; the zero value is ready to use. |
| `(*Pool) Get() *Buffer` | Get an empty buffer attached to the pool. |
| `NewBuffer(data []byte) *Buffer` | Create a detached buffer adopting `data` (no copy). |
| `(*Buffer) Write / WriteString / WriteByte` | Append bytes / a string / one byte. |
| `(*Buffer) Read / ReadByte / WriteTo` | Consume the unread portion. |
| `(*Buffer) Next(n int) []byte` | Consume the next n bytes zero-copy (aliasing). |
| `(*Buffer) ReadFrom` | Fill from an `io.Reader` until EOF. |
| `(*Buffer) Bytes / String` | Unread bytes (aliasing) / unread string (copy). |
| `(*Buffer) Len / Size / Cap` | Unread length / total length / capacity. |
| `(*Buffer) Grow(n int)` | Pre-allocate space for `n` more bytes. |
| `(*Buffer) Rewind / Reset` | Rewind read position / truncate for reuse. |
| `(*Buffer) Wipe()` | Zero the whole backing array, then reset. |
| `ErrTooLarge` | Panic value when a buffer cannot grow further. |
| `(*Buffer) SetBytes(p []byte)` | Replace contents, adopting `p` (no copy), and rewind. |
| `(*Buffer) Scratch() []byte` | Spare capacity as a full-length slice (aliasing) — the `dst` for decode-into APIs. |
| `(*Buffer) Fill(fn func([]byte) ([]byte, error)) error` | Decode into `Scratch`, adopt the result, return `fn`'s error. |
| `(*Buffer) Release() / Close() error` | Release into the pool. |
| `(*Buffer) Detach()` | Detach from the pool; Release/Close become no-ops. |
| `ReadAllBytes(r io.Reader) ([]byte, error)` | Read all bytes, zero-copy for `*Buffer`. |

See the [Go doc comments](buf.go) for the full details of each call.

## Performance

Pooling costs a single small allocation per `Get`/`Release` cycle (the buffer
handle itself); the backing arrays and pool bookkeeping are fully reused, and
the write path is allocation-free once capacity is established:

```text
BenchmarkGetRelease-16            38.95 ns/op    64 B/op    1 allocs/op
BenchmarkGetReleaseParallel-16    23.21 ns/op    64 B/op    1 allocs/op
BenchmarkWrite-16                 34.81 ns/op     0 B/op    0 allocs/op
```

Run them with `go test -bench=. -benchmem`. For comparisons against
`bytes.Buffer`+`sync.Pool`, `valyala/bytebufferpool` and `oxtoacart/bpool` —
including the memory-retention behavior the strike heuristic exists for — see
[COMPARISON.md](COMPARISON.md); the harness lives in [`_bench/`](_bench/).

## License

[MIT](LICENSE)
