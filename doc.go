// Package bufpool provides pooled, reusable byte buffers that reduce
// allocations and garbage-collector pressure in code handling many
// short-lived buffers.
//
// A Buffer implements io.Reader, io.Writer, io.StringWriter, io.ReaderFrom,
// io.WriterTo, io.Closer and fmt.Stringer. Writes append to the buffer; reads
// consume it from the front, tracked by an internal read position. Buffers
// obtained from a Pool (whose zero value is ready to use) are returned to it
// with Release (or Close), after which they must not be used.
//
// Releasing transfers the backing array back to the pool, so slices obtained
// through Bytes or ReadAllBytes are invalidated by Release, Close and Reset;
// conversely, slices handed to NewBuffer or SetBytes are adopted as the
// buffer's backing array (and follow it into the pool when it is released),
// so the caller must not use them afterwards.
//
// To keep pooled memory bounded, an adaptive strike heuristic decides on each
// Release or Reset whether a buffer's backing array is worth keeping: arrays
// of at most 64 KiB, or at least 50% utilized, are always kept; an oversized,
// under-utilized array survives up to four consecutive strikes before it is
// discarded. This prevents a single large usage from pinning memory through a
// continuous stream of small ones.
//
// Adapted from https://github.com/golang/go/issues/27735#issuecomment-739169121.
package bufpool
