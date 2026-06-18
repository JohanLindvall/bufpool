# bufpool

A small Go package for pooling and reusing byte buffers, reducing allocations
and garbage-collector pressure in code that handles many short-lived buffers.

A `Buffer` implements `io.Reader`, `io.Writer`, `io.StringWriter`,
`io.WriterTo` and `io.Closer`, so it drops into most code that already speaks
the standard streaming interfaces. Buffers obtained from a `Pool` can be
returned to it for reuse, and an adaptive *strike* heuristic discards backing
arrays that have grown large but are repeatedly under-utilized, so a single
large write does not pin memory indefinitely.

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
pool := bufpool.New()

// Get a buffer from the pool. The second return value is the (empty)
// backing slice and may be ignored.
buf, _ := pool.Get()

buf.WriteString("hello ")
buf.Write([]byte("world"))

// Use it as an io.Reader.
data, _ := bufpool.ReadAllBytes(buf) // []byte("hello world"), zero-copy for *Buffer

// Return the buffer to the pool for reuse. Do not use buf afterwards.
buf.Return()
```

Because a `Buffer` is an `io.Closer`, it also works with `defer`:

```go
buf, _ := pool.Get()
defer buf.Close() // equivalent to buf.Return()
```

## Usage

### Pooling

```go
pool := bufpool.New()

// Empty buffer.
buf, _ := pool.Get()

// Buffer pre-filled with a copy of some data.
buf, _ = pool.GetFrom([]byte("payload"))
```

Calling `Return` (or `Close`) recycles the buffer into the pool and resets it to
the zero value, so it must not be used afterwards. Re-acquire one with `Get`.

### Reading and writing

A `Buffer` separates writes (which append) from reads (which consume from the
front, tracked by an internal read position):

```go
buf, _ := pool.Get()
buf.WriteString("abcdef")

p := make([]byte, 3)
buf.Read(p)            // p = "abc", read position now at 3
buf.Bytes()            // []byte("def") — the unread remainder
buf.Len()              // 6 — total length, including consumed bytes

buf.Rewind()           // reset the read position to re-read from the start
```

`WriteTo` streams the unread portion to any `io.Writer`, and `ReadAllBytes`
returns the unread bytes — without copying when given a `*Buffer`.

### Detached buffers

A `Buffer` can be used standalone, without a pool, via `NewBuffer`. Its initial
bytes become the backing array directly (they are not copied):

```go
buf := bufpool.NewBuffer([]byte("seed")...)
buf.WriteString(" more")
```

A detached buffer's `Return`/`Close` are no-ops. Use `Pool.Attach` to attach one
to a pool, and `Buffer.Detach` to remove it again:

```go
buf := bufpool.NewBuffer()
if err := pool.Attach(buf); err != nil {
    // already attached to a pool
}
buf.Detach()
```

### In-place reuse

`Reset` rewinds the read position and truncates the buffer for reuse while
keeping it attached to its pool — useful in a tight loop where you want to reuse
the same `Buffer` without round-tripping through `Get`/`Return`:

```go
buf, _ := pool.Get()
for _, item := range items {
    buf.Reset()
    buf.WriteString(item)
    // ... use buf ...
}
buf.Return()
```

## The strike heuristic

To avoid keeping unnecessarily large backing arrays alive, both `Return` and
`Reset` apply the same heuristic when deciding whether to keep a buffer's
backing array:

- Buffers with capacity ≤ 64 KiB are always kept (strike counter cleared).
- Buffers that are at least 50% utilized are always kept (strike counter
  cleared).
- An oversized, under-utilized buffer is given up to four consecutive *strikes*;
  on the fifth it is discarded and replaced with a fresh, empty backing array.

This means a single large usage is not kept alive forever by a continuous stream
of small ones, while transient large usages are still tolerated. `Return`
returns the resulting strike count, which is mainly useful for tests and tuning.

## API overview

| Symbol | Description |
| --- | --- |
| `New() *Pool` | Create a new, empty pool. |
| `(*Pool) Get() (*Buffer, []byte)` | Get an empty buffer attached to the pool. |
| `(*Pool) GetFrom(data []byte) (*Buffer, []byte)` | Get a buffer pre-filled with a copy of `data`. |
| `(*Pool) Attach(b *Buffer) error` | Attach a detached buffer to the pool. |
| `NewBuffer(in ...byte) *Buffer` | Create a detached buffer (no pool). |
| `(*Buffer) Write / WriteString` | Append bytes / a string. |
| `(*Buffer) Read / WriteTo` | Consume the unread portion. |
| `(*Buffer) Bytes / Len` | Unread bytes / total length. |
| `(*Buffer) Rewind / Reset` | Rewind read position / truncate for reuse. |
| `(*Buffer) SetBytes(p []byte)` | Replace contents (no copy). |
| `(*Buffer) Return() int / Close() error` | Recycle into the pool. |
| `(*Buffer) Detach()` | Detach from the pool. |
| `ReadAllBytes(r io.Reader) ([]byte, error)` | Read all bytes, zero-copy for `*Buffer`. |

See the [Go doc comments](bufpool.go) for the full details of each call.

## License

See the repository for license information.
