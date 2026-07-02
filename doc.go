// Package bufpool provides pooled, reusable byte buffers that reduce
// allocations and garbage-collector pressure in code handling many
// short-lived buffers.
//
// A Buffer implements io.Reader, io.Writer, io.StringWriter, io.WriterTo and
// io.Closer. Writes append to the buffer; reads consume it from the front,
// tracked by an internal read position. Buffers obtained from a Pool are
// returned to it with Recycle (or Close), after which they must not be used.
//
// To keep pooled memory bounded, an adaptive strike heuristic decides on each
// Recycle or Reset whether a buffer's backing array is worth keeping: arrays
// of at most 64 KiB, or at least 50% utilized, are always kept; an oversized,
// under-utilized array survives up to four consecutive strikes before it is
// discarded. This prevents a single large usage from pinning memory through a
// continuous stream of small ones.
//
// Adapted from https://github.com/golang/go/issues/27735#issuecomment-739169121.
package bufpool
