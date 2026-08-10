# bufpool vs the alternatives

How `bufpool` compares with the common ways of pooling byte buffers in Go.
All numbers were measured with the harness in [`_bench/`](_bench/) on
go1.26.5, linux/amd64, AMD Ryzen 7 8840HS (16 threads); reproduce with:

```sh
cd _bench
go test -bench . -benchmem -count 6   # speed
go run ./retention -impl bufpool      # memory retention, one impl per run
go test -fuzz FuzzDifferential .      # bytes.Buffer semantics equivalence
```

## Contenders

| | Description | Version |
| --- | --- | --- |
| **bufpool** | this package | — |
| **bytes.Buffer + sync.Pool** | the stdlib idiom: `sync.Pool` of `*bytes.Buffer`, `Reset` on reuse | stdlib |
| **[]byte + sync.Pool** | the rawest idiom: `sync.Pool` of `*[]byte`, re-sliced to `[:0]` | stdlib |
| **[valyala/bytebufferpool](https://github.com/valyala/bytebufferpool)** | popular pool with percentile-based size calibration | v1.0.0 |
| **[oxtoacart/bpool](https://github.com/oxtoacart/bpool)** | channel-based bounded pool of `bytes.Buffer` | 2019 snapshot |

## Feature comparison

| | bufpool | bytes.Buffer + sync.Pool | []byte + sync.Pool | bytebufferpool | bpool |
| --- | --- | --- | --- | --- | --- |
| Read-position tracking (`io.Reader` that consumes) | ✅ | ✅ | ❌ manual | ❌ | ✅ |
| `io.ReaderFrom` / `io.WriterTo` | ✅ / ✅ | ✅ / ✅ | ❌ | ✅ / ✅ | ✅ / ✅ |
| Release without a pool reference in scope (`io.Closer`) | ✅ `Close` releases | ❌ | ❌ | ❌ `Put(pool, b)` | ❌ |
| Use-after-release protection | ✅ handle is zeroed; becomes a benign no-op buffer | ❌ silent corruption | ❌ silent corruption | ❌ silent corruption | ❌ silent corruption |
| Copy-by-value protection | ✅ `go vet` flags | ❌ | ❌ | ❌ | ❌ |
| Oversized-buffer eviction | ✅ strike heuristic, immediate | ❌ only on GC, and only if idle | ❌ same | ✅ after 42k-call calibration | ❌ never (bounded count, not size) |
| Zero-value pool | ✅ | ✅ | ✅ | ✅ | ❌ constructor + size |
| Cost per get/release cycle | 1 alloc (64 B handle) | 0 | 0 | 0 | 0 |

## Speed

Get → write → release cycle, ns/op (median of 6 runs, ±≤8% unless noted):

| scenario | bufpool | bytes.Buffer+Pool | []byte+Pool | bytebufferpool | bpool |
| --- | --- | --- | --- | --- | --- |
| 11 B, serial | 40.6 | 12.6 | **11.7** | 14.3 | 26.0 |
| 4 KiB, serial | 68.7 | 37.4 | **36.8** | 39.4 | 50.8 |
| 11 B, parallel | 27.7 | **11.1** | 11.4 | 23.5 ±16% | 54.6 |
| 4 KiB, parallel | 34.9 | 14.1 | **14.0** | 30.4 ±13% | 153.1 |

The raw `sync.Pool` idioms win on pure cycle latency: bufpool pays ~15–30 ns
extra per cycle (less under parallel contention, more serially), which is
mostly the one 64 B handle allocation that funds its
use-after-release poisoning (`Release` zeroes the handle, so a stale buffer
becomes a harmless detached no-op instead of silently corrupting pooled
memory). The write path itself is allocation-free and identical in cost to
`bytes.Buffer` (~115 GB/s appending 4 KiB chunks). In workloads that do real
work per buffer, tens of nanoseconds per cycle is noise; if it is not noise in
yours, use a raw `sync.Pool`.

## Memory retention

The scenario buffer pools get wrong: a workload of 1 KiB requests with an
occasional 1 MiB spike (every 1000th request; 100k cycles). A naive pool keeps
handing the spike's 1 MiB array to every subsequent small request — under low
allocation pressure the GC (which is what clears a `sync.Pool`) almost never
runs, so the oversized array is effectively pinned forever. With N workers
that is N × max-size resident memory instead of N × working-size.

| | small cycles served by the pinned 1 MiB array | alloc churn | GC cycles |
| --- | --- | --- | --- |
| **bufpool** | **0.5% (≤ 5 per spike, bounded by design)** | 106 MB | 33 |
| bytes.Buffer + sync.Pool | 99.0% — pinned indefinitely | 1 MB | 0 |
| []byte + sync.Pool | 99.0% — pinned indefinitely | 1 MB | 0 |
| bytebufferpool | 41.0% — pinned until its 42k-call calibration completes | 59 MB | 19 |
| bpool | 99.0% — pinned indefinitely | 1 MB | 0 |

bufpool's strike heuristic caps the damage at five under-utilized cycles per
spike, at the honest cost of re-allocating the large array on each spike (the
churn column — ~1 MiB per spike here). bytebufferpool eventually adapts, but
pins for its entire calibration window and re-pins after each recalibration
reset. The `sync.Pool` idioms and bpool never evict by size at all.

**Read that bound as per-spike, not steady-state, and not the whole retention
story.** The measured "≤ 5 per spike" holds because the harness releases each
buffer without `Reset` and the spikes are rare. Two other shapes behave
differently, and in one of them bufpool is the *worse* option: a large release
that is well utilized clears the strike counter, so a large user recurring more
often than once per five small ones keeps its array resident indefinitely; and
because reads never reclaim the consumed prefix, a single buffer used as a
long-lived FIFO grows without bound and then hands that oversized array to the
pool — see divergence 2 under [Semantics](#semantics). `Reset` at message
boundaries is what bounds both.

## Semantics

`bufpool.Buffer` is differentially fuzzed against `bytes.Buffer` (see
`_bench/fuzz_test.go`): millions of random `Write`/`WriteString`/`WriteByte`/
`Read`/`ReadByte`/`Next`/`Len`/`Bytes`/`WriteTo`/`Reset` programs execute
identically on both, so code ported from `bytes.Buffer` keeps its behavior for
the fuzzed surface. The fuzzer compares contents, lengths and return values —
it never calls `Cap`, so capacity and retention behavior is outside what it
checks even for the operations it drives.

Four known divergences, all outside that surface:

1. **Zero-length `Read` on a drained buffer** returns `(0, io.EOF)` here where
   `bytes.Buffer` returns `(0, nil)` — both legal under `io.Reader`, which
   leaves the empty-slice case unspecified.
2. **Reads never reclaim the consumed prefix.** `bytes.Buffer` compacts on
   read, so a drain-and-refill loop holds steady; here the buffer grows with
   the total bytes streamed through it. 1000 × (write 4 KiB, drain) with no
   `Reset` ends at `Cap` 4194304 against `bytes.Buffer`'s 4096. This is
   deliberate — it is what makes `Rewind` able to replay everything ever
   written — and it applies to `WriteTo` as well as `Read`. See
   [Memory retention](#memory-retention) below for what it means inside a pool,
   and the "Streaming" section of the README for how to bound it.
3. **`Reset` applies the eviction heuristic**, where `bytes.Buffer.Reset` only
   truncates. An oversized, repeatedly under-filled buffer therefore loses its
   backing array on the fifth consecutive `Reset` — including capacity that an
   explicit `Grow` reserved, and including on a detached buffer with no pool
   involved.
4. **`Next` and `ReadAllBytes` return slices capped to their length.**
   `bytes.Buffer` returns slices whose capacity runs to the end of the backing
   array, so appending to a `Next` slice there overwrites the bytes not yet
   read; here the append allocates instead. `Bytes` is *not* capped — it
   matches `bytes.Buffer.Bytes`, so the hand-spare-capacity-to-an-encoder
   idiom ports unchanged, and so do its overwrite hazards, compounded here by
   the pool reusing the array after `Release` (see "Ownership and aliasing" in
   the README).

`ErrTooLarge` mirrors `bytes.ErrTooLarge`: both are exported error values passed
to `panic` when the buffer cannot grow, so the recover-and-classify idiom ports
across unchanged.

## When to use what

- **bufpool** — buffers with reader semantics (parse-as-you-go, `io.Copy`
  in/out, HTTP request/response bodies), workloads with size variance where
  memory bounding matters, or code where use-after-release must fail safe.
  The `io.ReadCloser` handoff (`Close` releases, no pool reference needed at
  the release site) is unique among these options.
- **bytes.Buffer + sync.Pool** — uniform sizes, hot paths counted in
  nanoseconds, and the discipline to never leak a buffer reference past `Put`.
- **[]byte + sync.Pool** — same, when you only append and never read.
- **bytebufferpool** — append-only workloads wanting automatic size policy
  without a handle allocation; accept the calibration lag.
- **bpool** — bounded *count* (backpressure) is the actual requirement; it is
  otherwise dominated in these measurements.
