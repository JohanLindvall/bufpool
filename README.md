# bufpool

A small Go package for pooling and reusing byte buffers, reducing allocations
and garbage-collector pressure in code that handles many short-lived buffers.

A `Buffer` implements `io.Reader`, `io.Writer`, `io.StringWriter`,
`io.ReaderFrom`, `io.WriterTo`, `io.Closer` and `fmt.Stringer`, so it drops
into most code that already speaks the standard streaming interfaces. Buffers
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
buf.String()           // "def" — a copy, safe to keep after release
buf.Len()              // 3 — unread bytes, like bytes.Buffer.Len
buf.Size()             // 6 — total written length, including consumed bytes
buf.Cap()              // capacity of the backing array

buf.Rewind()           // reset the read position to re-read from the start
```

`WriteTo` streams the unread portion to any `io.Writer`, and `ReadFrom` fills
the buffer from any `io.Reader`, so `io.Copy` in either direction avoids
intermediate copy buffers:

```go
n, err := io.Copy(buf, resp.Body) // uses buf.ReadFrom, no 32 KiB scratch buffer
```

When the output size is known in advance, `Grow` pre-allocates capacity so
subsequent writes do not reallocate:

```go
buf.Grow(len(payload))
buf.Write(payload)
```

### Ownership and aliasing

The zero-copy calls trade safety for speed; their rules are:

- `Bytes` and `ReadAllBytes` return slices that **alias** the buffer.
  `Release`, `Close` and `Reset` invalidate them — the backing array re-enters
  the pool and the next `Get` may overwrite it. Copy the bytes (or use
  `String`) if they must outlive the buffer.
- `NewBuffer` and `SetBytes` **adopt** the given slice as the backing array
  without copying. Ownership transfers to the buffer (and, once released, to
  the pool): the caller must not use the slice afterwards.

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
  on the fifth it is discarded and replaced with a fresh, empty backing array.

This means a single large usage is not kept alive forever by a continuous stream
of small ones, while transient large usages are still tolerated.

## API overview

| Symbol | Description |
| --- | --- |
| `Pool` | Buffer pool; the zero value is ready to use. |
| `(*Pool) Get() *Buffer` | Get an empty buffer attached to the pool. |
| `NewBuffer(data []byte) *Buffer` | Create a detached buffer adopting `data` (no copy). |
| `(*Buffer) Write / WriteString` | Append bytes / a string. |
| `(*Buffer) Read / WriteTo` | Consume the unread portion. |
| `(*Buffer) ReadFrom` | Fill from an `io.Reader` until EOF. |
| `(*Buffer) Bytes / String` | Unread bytes (aliasing) / unread string (copy). |
| `(*Buffer) Len / Size / Cap` | Unread length / total length / capacity. |
| `(*Buffer) Grow(n int)` | Pre-allocate space for `n` more bytes. |
| `(*Buffer) Rewind / Reset` | Rewind read position / truncate for reuse. |
| `(*Buffer) SetBytes(p []byte)` | Replace contents, adopting `p` (no copy), and rewind. |
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
