// SPDX-License-Identifier: MIT

// Package bufpool pools bytes.Buffer-like byte buffers, evicting oversized,
// under-used backing arrays so that one large request does not pin memory for
// the small ones after it. Pooling cuts allocations and garbage-collector
// pressure in code that handles many short-lived buffers.
//
// A Buffer implements io.Reader, io.ByteReader, io.Writer, io.ByteWriter,
// io.StringWriter, io.ReaderFrom, io.WriterTo, io.Closer and fmt.Stringer.
// Writes append to the buffer; reads consume it from the front, tracked by an
// internal read position. Buffers obtained from a Pool (whose zero value is
// ready to use) are returned to it with Release (or Close), after which they
// must not be used. A Pool is safe for concurrent use; an individual Buffer
// is not.
//
// Reads never reclaim memory: the consumed prefix stays in place so Rewind
// can replay the full contents, and writes append after it. A buffer used as
// a long-lived FIFO (write, read, repeat) therefore grows with the total
// bytes streamed through it, not the working set. Bound it by calling Reset
// at natural message boundaries. A Release and Get round trip does not bound
// it: Get re-slices the same array to zero length, so the round trip resets
// Size but not Cap.
//
// That growth outlives the buffer. A FIFO buffer that grew to N bytes hands
// an N-byte array to the pool on Release, where it scores as well utilized and
// is handed on to unrelated callers regardless of how little they asked for,
// so Reset bounds pool-wide memory rather than just one buffer's.
//
// Releasing transfers the backing array back to the pool, so slices obtained
// through Bytes, Next, ReadAllBytes or Scratch are invalidated by Release,
// Close and Reset; conversely, slices handed to NewBuffer or SetBytes are
// adopted as the buffer's backing array (and follow it into the pool when it
// is released), so the caller must not use them afterwards. Scratch exposes
// the spare capacity as a full-length slice for APIs that decode into a
// caller-provided destination, and Fill wraps the whole idiom — scratch,
// decode, adopt — in one call whose closure keeps every aliasing slice out of
// the caller's scope (see Scratch and Fill). Slices returned by Next and
// ReadAllBytes are capped to their length, unlike the equivalents on
// bytes.Buffer, so appending to one allocates instead of overwriting the
// bytes that follow. Bytes is not capped: as on bytes.Buffer, its slice
// carries the array's spare capacity so an appending encoder can fill the
// buffer's slack — but that makes it a writable window onto the buffer, and
// the buffer must not otherwise be modified through it (see Bytes).
//
// Nothing on that path clears an array, so a released buffer's bytes stay
// readable to whichever unrelated caller receives it next — including through
// the spare capacity that Scratch exposes and ReadFrom hands to an io.Reader.
// Buffer.Wipe zeroes
// the whole array and is the opt-in for buffers that held secrets.
//
// To keep pooled memory bounded, an adaptive strike heuristic decides on each
// Release or Reset whether a buffer's backing array is worth keeping: buffers
// whose capacity is at most 64 KiB, or which are at least 50% utilized, are
// always kept and have their strike counter cleared; an oversized,
// under-utilized buffer survives up to four consecutive strikes before it is
// discarded, meaning it is left with no backing array at all rather than a
// fresh one. This prevents a single large usage from pinning memory through a
// continuous stream of small ones. Two consequences follow from the details.
// The budget is spent only by consecutive under-utilized applications, so a
// large but well-utilized release recurring more often than once per five
// small ones clears the counter every time and keeps its array resident
// indefinitely. And each Reset and each Release is one application, so a Reset
// immediately before a Release charges two strikes for one use.
//
// Utilization is measured by capacity, which Go reports per slice rather than
// per array, and against the written length rather than the unread length.
// Both matter in practice: adopting a capacity-capped sub-slice of a large
// array (see SetBytes) hides the array's real size from the heuristic, and
// capacity reserved by Grow but never filled counts against utilization.
//
// Adapted from https://github.com/golang/go/issues/27735#issuecomment-739169121.
package bufpool
