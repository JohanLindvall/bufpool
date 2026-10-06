// SPDX-License-Identifier: MIT

package bufpool

import (
	"errors"
	"fmt"
	"io"
)

// minRead is the minimum spare capacity ReadFrom ensures before each Read
// from the source, mirroring bytes.MinRead.
const minRead = 512

// ErrTooLarge is the value passed to panic when a buffer cannot grow to hold
// the requested data, mirroring bytes.ErrTooLarge. It is an error rather than
// a plain string so that the recover-and-classify idiom works:
//
//	defer func() {
//		if r := recover(); r != nil {
//			err, ok := r.(error)
//			if !ok || !errors.Is(err, bufpool.ErrTooLarge) {
//				panic(r) // not ours; let it go
//			}
//			// handle
//		}
//	}()
//
// The panics that report programmer errors rather than resource exhaustion —
// a negative count, or an io.Writer or io.Reader violating its contract — stay
// plain strings, as they are in the bytes package.
var ErrTooLarge = errors.New("bufpool.Buffer: too large")

// Buffer is a byte buffer that may be attached to a Pool. It implements
// io.Reader, io.ByteReader, io.Writer, io.ByteWriter, io.StringWriter,
// io.ReaderFrom, io.WriterTo, io.Closer and fmt.Stringer. Writes append to
// the buffer; reads consume it from the front, tracked by an internal read
// position. The zero value is a usable, detached buffer.
//
// Reads never reclaim memory: the consumed prefix stays in place so Rewind
// can replay it, and writes append after it. A buffer used as a long-lived
// FIFO (write, read, repeat) therefore grows with the total bytes streamed
// through it, not the working set; bound it by calling Reset at natural
// message boundaries. A Release and Get round trip does not bound it, because
// Get re-slices the same array to zero length: see the package documentation,
// which is the authoritative description of retention and the keep-or-discard
// policy.
//
// A Buffer must not be copied after first use (go vet reports such copies),
// and must not be used after Release or Close. Unlike a Pool, a Buffer is not
// safe for concurrent use by multiple goroutines.
type Buffer struct {
	noCopy noCopy
	poolStorage
	readPos int
	pool    *Pool
	storage *poolStorage // pooled object, reused on Release; nil if never pooled
}

var _ interface {
	io.Reader
	io.ByteReader
	io.Writer
	io.ByteWriter
	io.StringWriter
	io.ReaderFrom
	io.WriterTo
	io.Closer
	fmt.Stringer
} = (*Buffer)(nil)

// abandon marks the current backing array as given up: the strike counter
// belongs to the array and must not carry over to its replacement, and the
// pooled storage's stale copy of the slice header must not keep the array
// reachable through b.storage until the next Release.
func (b *Buffer) abandon() {
	b.strikes = 0
	b.dropStorageRef()
}

// dropStorageRef clears the pooled storage's stale copy of the slice header
// so an abandoned array is not kept reachable through b.storage until the
// next Release. SetBytes calls it directly rather than through abandon: the
// header clear must stay unconditional even when the strike reset is not, or
// adopting a smaller foreign array would leave the old array pinned.
func (b *Buffer) dropStorageRef() {
	if b.storage != nil {
		b.storage.buf = nil
	}
}

// sameArray reports whether a and p share an uncapped backing array. Slices
// of one array whose capacities run to the array's end extend to the same
// final element, so comparing those elements' addresses identifies the array
// without unsafe. A capacity-capped sub-slice (arena[:n:n]) of the same array
// compares as different; callers treat "different" as the conservative
// answer.
func sameArray(a, p []byte) bool {
	return cap(a) > 0 && cap(p) > 0 && &a[:cap(a)][cap(a)-1] == &p[:cap(p)][cap(p)-1]
}

// beforeAppend prepares to append n more bytes: any capacity shortfall is
// grown via Grow so the append below never reallocates behind the pool's
// back.
func (b *Buffer) beforeAppend(n int) {
	if n > cap(b.buf)-len(b.buf) {
		b.Grow(n)
	}
}

// NewBuffer creates a new detached buffer whose initial contents are data. The
// slice becomes the buffer's backing array; it is not copied, and the caller
// should not use data after this call. NewBuffer(nil) creates an empty buffer.
func NewBuffer(data []byte) *Buffer {
	return &Buffer{poolStorage: poolStorage{buf: data}}
}

// Detach detaches the buffer from its pool, making Release and Close no-ops.
// Use it to let a buffer's contents safely outlive a consumer that closes it.
func (b *Buffer) Detach() {
	b.pool = nil
}

// SetBytes replaces the buffer's contents with p and rewinds the read
// position. The slice becomes the new backing array; it is not copied, so
// ownership of p transfers to the buffer (and, once released, to its pool)
// and the caller must not use p after this call.
//
// The keep-or-discard heuristic sizes a buffer by cap(p) alone, because Go
// cannot recover an array's size from a slice. Adopting a capacity-capped
// sub-slice of a much larger array (arena[:n:n]) therefore hands the pool the
// whole array while the heuristic classifies it by the small capacity, and it
// is never evicted. Pass a full-capacity slice, or bytes.Clone it, when the
// underlying array is substantially larger than the contents.
//
// Strikes belong to the backing array, so adopting a slice of the current
// array — as Fill does when the decoder used the scratch space — preserves
// the array's strike history, while adopting a foreign array resets it. A
// capacity-capped sub-slice of the current array cannot be told apart from a
// foreign array and conservatively resets it too.
func (b *Buffer) SetBytes(p []byte) {
	b.readPos = 0
	if !sameArray(b.buf, p) {
		b.strikes = 0
	}
	b.dropStorageRef()
	b.buf = p
}

// Fill passes the buffer's spare capacity (Scratch) to fn, adopts the slice
// fn returns as the buffer's new contents (SetBytes), and returns fn's error
// unchanged. It packages the decode-into-dst idiom in one call:
//
//	buf := pool.Get()
//	err := buf.Fill(func(dst []byte) ([]byte, error) {
//		return snappy.Decode(dst, packed)
//	})
//
// fn should treat dst as scratch space of arbitrary length and content: use
// it when it is large enough, allocate otherwise, and return the slice
// holding the result — the contract of snappy.Decode and kin. Either way the
// buffer ends up owning the result: in place with no allocation when dst
// sufficed, or adopting fn's fresh exact-size array, which warms the pool for
// the next round trip. Call Grow first to guarantee the in-place path.
//
// Neither dst nor the returned slice may be retained or used once fn returns
// — both alias the buffer's backing array; keeping everything inside fn is
// the point of Fill over calling Scratch and SetBytes directly. The result is
// adopted even when fn returns an error (decoders return a nil or partial
// result alongside their error, leaving the buffer empty or holding the
// partial result), so release or reset the buffer on error as usual. Use Fill
// on an empty buffer: adoption discards existing contents.
func (b *Buffer) Fill(fn func(scratch []byte) ([]byte, error)) error {
	p, err := fn(b.Scratch())
	b.SetBytes(p)
	return err
}

// Scratch returns the buffer's spare capacity — the region from its length to
// its capacity — as a full-length slice. It is the destination-slice
// counterpart of the Bytes append idiom: pass it as the dst of an API that
// fills a caller-provided slice when it is long enough and otherwise
// allocates, such as snappy.Decode, then adopt the result with SetBytes —
// or use Fill, which wraps the whole sequence. Such APIs key the reuse
// decision on len(dst) (github.com/golang/snappy) or on cap(dst)
// (klauspost/compress's s2); the full-length slice satisfies both, where a
// fresh pooled buffer's Bytes — empty, however large its capacity — is
// silently ignored by the len-based ones. Both outcomes of the decode adopt correctly: an
// in-place result re-slices the buffer's own array with no allocation, and a
// fresh exact-size array replaces it and warms the pool for the next round
// trip. Call Grow first to guarantee the in-place path.
//
// Scratch is not a write path: bytes written into the slice are invisible to
// the buffer — its length does not change — until the result is adopted. Use
// it on an empty buffer: adoption discards existing contents, and adopting an
// in-place result on a partly-written buffer strands the written prefix in
// the array. The slice aliases the backing array, so it is only valid until
// the next mutating call, and on a buffer from a pool it initially holds
// bytes left by a previous, unrelated user of the pool (Wipe clears an array
// before it re-enters the pool).
func (b *Buffer) Scratch() []byte {
	return b.buf[len(b.buf):cap(b.buf)]
}

// Bytes returns the unread portion of the buffer. The slice aliases the
// buffer's backing array and is only valid until the next mutating call;
// Release, Close and Reset invalidate it. Copy the bytes (or use String) if
// they must outlive the buffer.
//
// Like bytes.Buffer.Bytes, the slice's capacity runs to the end of the
// backing array, so appending to it writes into the buffer's spare capacity
// without allocating. That exists for one idiom — hand the spare capacity to
// an appending encoder and adopt the result:
//
//	p := enc.MarshalAppend(buf.Bytes(), msg) // fills the buffer's slack
//
// It also makes the slice a writable window onto the buffer itself, so do not
// modify the buffer through it in any other pattern. Appended bytes lie beyond
// the buffer's length: the buffer neither sees nor preserves them, its next
// write lands directly over them, and Release hands the array — appended bytes
// included — to whichever unrelated caller gets it from the pool next. Finish
// with, or copy, an appended result before writing to, releasing or resetting
// the buffer. Next and ReadAllBytes still cap their slices to their length.
func (b *Buffer) Bytes() []byte {
	return b.buf[b.readPos:]
}

// String returns a copy of the unread portion of the buffer as a string,
// implementing fmt.Stringer. If b is nil, it returns "<nil>".
func (b *Buffer) String() string {
	if b == nil {
		return "<nil>"
	}
	return string(b.buf[b.readPos:])
}

// Release returns the buffer to its pool and resets it to the zero value.
// Releasing invalidates all slices previously returned by Bytes, Next,
// ReadAllBytes or Scratch: the backing array re-enters the pool and the next Get may
// overwrite it, so copy such slices first if they must outlive the buffer.
// After Release the buffer is a detached zero buffer — further calls operate
// on that empty buffer instead of panicking, but are programming errors. If
// the buffer is detached, Release is a no-op. The cost of a Get/Release
// round-trip is the single small allocation of the Buffer handle in Get.
func (b *Buffer) Release() {
	if b.pool == nil {
		return
	}
	// A pool-attached buffer always carries the pooled storage it came with.
	*b.storage = b.poolStorage
	b.pool.put(b.storage)
	*b = Buffer{}
}

// Len returns the number of unread bytes in the buffer, matching the semantics
// of bytes.Buffer.Len. Use Size for the total written length.
func (b *Buffer) Len() int {
	return len(b.buf) - b.readPos
}

// Size returns the total length of the buffer, including any portion already
// consumed by Read.
func (b *Buffer) Size() int {
	return len(b.buf)
}

// Cap returns the capacity of the buffer's backing array: the total space,
// including the already-written portion, that can be used before another
// allocation.
func (b *Buffer) Cap() int {
	return cap(b.buf)
}

// Grow grows the buffer's capacity, if necessary, to guarantee space for
// another n bytes: after Grow(n), at least n bytes can be written without
// another allocation. When it does allocate, Grow over-allocates (at least
// doubling the capacity) so that repeated grow-and-fill cycles stay amortized
// O(n) rather than reallocating on every round. Grow panics with
// "bufpool.Buffer.Grow: negative count" if n is negative, and with
// ErrTooLarge if the buffer would grow beyond the maximum slice length.
//
// Reserved capacity is not exempt from the keep-or-discard heuristic, which
// measures utilization as written length against capacity: capacity above
// 64 KiB that is reserved but left unfilled counts as under-utilized, so a
// buffer that reserves far more than it writes loses the reservation on the
// fifth consecutive Release or Reset. Reserve close to what will be written,
// or expect to pay for the reservation again every fifth cycle.
func (b *Buffer) Grow(n int) {
	if n < 0 {
		panic("bufpool.Buffer.Grow: negative count")
	}
	if cap(b.buf)-len(b.buf) >= n {
		return
	}
	need := len(b.buf) + n
	if need < 0 { // int overflow
		panic(ErrTooLarge)
	}
	// 2*cap may overflow to negative; max then falls back to the exact need.
	// 64 mirrors bytes.Buffer's smallBufferSize, skipping the tiny first steps
	// of the doubling ladder.
	buf := makeBuf(len(b.buf), max(need, 2*cap(b.buf), 64))
	copy(buf, b.buf)
	b.abandon()
	b.buf = buf
}

// makeBuf allocates a backing array, converting the runtime's allocation-size
// panic (beyond the maximum slice length) into ErrTooLarge, mirroring
// bytes.growSlice.
//
// The append-make pattern is deliberate: make([]byte, length, capacity)
// reports the capacity that was asked for, while the allocator has already
// reserved roundupsize(capacity) bytes. Appending instead surfaces that
// rounded capacity, so the size-class slack is usable rather than wasted and
// the next write past the requested size does not reallocate. bytes.growSlice
// does the same; like it, this relies on runtime allocator behaviour that is
// not part of the language spec (go.dev/issue/51462). The cost is that the
// whole capacity is zeroed, where make of a zero-length slice can skip the
// memclr on a fresh span.
func makeBuf(length, capacity int) (buf []byte) {
	defer func() {
		if recover() != nil {
			panic(ErrTooLarge)
		}
	}()
	b := append([]byte(nil), make([]byte, capacity)...)
	return b[:length]
}

// Close returns the buffer to its pool and always returns a nil error. It
// implements io.Closer and is equivalent to Release, so a pooled *Buffer can
// be handed off as an io.ReadCloser and is returned to the pool at the release site
// without a pool reference in scope.
//
// Handing a buffer to a consumer that closes it transfers release timing to
// that consumer, possibly on another goroutine: the caller's own Release
// becomes a no-op, and no slice from Bytes, Next, ReadAllBytes or Scratch may
// be held across the call. net/http is the common case and needs care. Because
// *Buffer already satisfies io.ReadCloser, http.NewRequest adopts it as
// req.Body verbatim rather than wrapping it, and the transport closes it
// before Do returns; and because the type switch there special-cases only
// *bytes.Buffer, *bytes.Reader and *strings.Reader, the request is sent with
// ContentLength -1 and Transfer-Encoding: chunked, with a nil GetBody that
// prevents a 307/308 redirect from replaying the body. These are separate
// problems and neither remedy covers both: Detach stops the transport's Close
// from returning the buffer to the pool, and setting req.ContentLength (plus a
// req.GetBody, if a redirect must replay the body) fixes the wire format. Do
// both. See the README for a worked example.
func (b *Buffer) Close() error {
	b.Release()
	return nil
}

// Read consumes up to len(p) unread bytes into p, advancing the read position.
// It returns io.EOF once the buffer is fully consumed. Read implements
// io.Reader.
func (b *Buffer) Read(p []byte) (int, error) {
	if b.readPos == len(b.buf) {
		return 0, io.EOF
	}
	n := copy(p, b.buf[b.readPos:])
	b.readPos += n
	return n, nil
}

// ReadByte consumes and returns the next unread byte, advancing the read
// position. It returns io.EOF once the buffer is fully consumed. ReadByte
// implements io.ByteReader.
func (b *Buffer) ReadByte() (byte, error) {
	if b.readPos == len(b.buf) {
		return 0, io.EOF
	}
	c := b.buf[b.readPos]
	b.readPos++
	return c, nil
}

// Next consumes the next n unread bytes and returns them as a slice, as if
// read by Read; if fewer than n bytes are unread, Next returns all of them.
// Like Bytes, the slice aliases the buffer's backing array and is only valid
// until the next mutating call. Next panics if n is negative.
//
// Unlike Bytes, the returned slice's capacity is limited to its length, so
// appending to it allocates rather than overwriting the bytes that follow —
// which, for Next, would be the unread remainder.
func (b *Buffer) Next(n int) []byte {
	if n > b.Len() {
		n = b.Len()
	}
	data := b.buf[b.readPos : b.readPos+n : b.readPos+n]
	b.readPos += n
	return data
}

// WriteTo writes the unread portion of the buffer to w, advancing the read
// position by the number of bytes accepted by w. If w accepts fewer bytes than
// offered without returning an error, WriteTo returns io.ErrShortWrite.
// WriteTo implements io.WriterTo.
func (b *Buffer) WriteTo(w io.Writer) (int64, error) {
	unread := b.buf[b.readPos:]
	if len(unread) == 0 {
		return 0, nil
	}
	nn, err := w.Write(unread)
	if nn < 0 || nn > len(unread) {
		panic("bufpool.Buffer.WriteTo: invalid Write count")
	}
	b.readPos += nn
	if err == nil && nn < len(unread) {
		err = io.ErrShortWrite
	}
	return int64(nn), err
}

// ReadFrom reads from r until EOF, appending to the buffer and growing it as
// needed. It returns the number of bytes read and any error except io.EOF
// encountered during the read. ReadFrom panics with ErrTooLarge if the buffer
// can no longer grow. ReadFrom implements io.ReaderFrom, so io.Copy into a
// Buffer needs no intermediate copy buffer.
//
// r is handed the buffer's spare capacity to read into. On a buffer that came
// from a pool that space may still hold bytes written by a previous, unrelated
// user of the pool, so a Reader that inspects or retains more of the slice
// than the n bytes it reports can observe them. Wipe clears an array before it
// re-enters the pool.
func (b *Buffer) ReadFrom(r io.Reader) (int64, error) {
	var total int64
	for {
		if cap(b.buf)-len(b.buf) < minRead {
			b.Grow(minRead)
		}
		n, err := r.Read(b.buf[len(b.buf):cap(b.buf)])
		if n < 0 {
			panic("bufpool.Buffer.ReadFrom: reader returned negative count from Read")
		}
		b.buf = b.buf[:len(b.buf)+n]
		total += int64(n)
		if err == io.EOF {
			return total, nil
		}
		if err != nil {
			return total, err
		}
	}
}

// Write appends p to the buffer, growing the backing array as needed. It always
// returns len(p) and a nil error, but panics with ErrTooLarge if the buffer
// can no longer grow. Write implements io.Writer.
func (b *Buffer) Write(p []byte) (int, error) {
	b.beforeAppend(len(p))
	b.buf = append(b.buf, p...)
	return len(p), nil
}

// WriteString appends s to the buffer without copying it into a temporary
// []byte first. It always returns len(s) and a nil error, but panics with
// ErrTooLarge if the buffer can no longer grow. WriteString implements
// io.StringWriter.
func (b *Buffer) WriteString(s string) (int, error) {
	b.beforeAppend(len(s))
	b.buf = append(b.buf, s...)
	return len(s), nil
}

// WriteByte appends c to the buffer, growing the backing array as needed. It
// always returns a nil error, but panics with ErrTooLarge if the buffer can no
// longer grow. WriteByte implements io.ByteWriter.
func (b *Buffer) WriteByte(c byte) error {
	b.beforeAppend(1)
	b.buf = append(b.buf, c)
	return nil
}

// Rewind resets the read position to zero so the buffer's full contents can
// be read again. It does not modify the contents. The replay is complete:
// reads and writes never discard the consumed prefix, so Rewind always
// reaches back to the last point the buffer was empty — its creation, or the
// most recent Reset or SetBytes. (Supporting this replay is why reads retain
// the consumed prefix; see the package documentation on streaming.)
func (b *Buffer) Rewind() {
	b.readPos = 0
}

// Reset rewinds the read position and truncates the buffer for in-place reuse,
// applying the same keep-or-discard heuristic the pool uses on Release: an oversized,
// repeatedly under-utilized backing array is dropped (replaced with a fresh nil
// buffer) instead of kept, so a single large use does not pin memory across
// resets. Unlike Release, the buffer stays usable and attached to its pool.
//
// Reset invalidates slices previously returned by Bytes, Next, ReadAllBytes
// or Scratch.
// It also counts as one application of the heuristic, as Release does, so a
// Reset immediately before a Release charges the array two strikes for one
// use; that is redundant, since Release resets the handle anyway.
func (b *Buffer) Reset() {
	b.readPos = 0
	if b.keep() {
		b.buf = b.buf[:0]
	} else {
		b.abandon() // drops the pooled copy of the slice header too
		b.buf = nil
	}
}

// Wipe zeroes the buffer's whole capacity — the consumed prefix, the unread
// bytes, and the spare capacity beyond them — then rewinds and truncates it.
//
// Nothing on the Release and Get path clears an array, so without Wipe a
// buffer's contents stay resident in the pool and are handed to the next,
// unrelated caller: readable through that buffer's spare capacity, and passed
// to any io.Reader that ReadFrom hands the spare capacity to. Call Wipe before
// Release on any buffer that held secrets:
//
//	buf := pool.Get()
//	defer func() { buf.Wipe(); buf.Release() }()
//
// Unlike Reset, Wipe does not apply the keep-or-discard heuristic, so the
// Release or Reset that follows it is still the single application — wiping
// does not charge the array twice. It does mean the buffer is empty by the
// time that application runs, so it scores 0% utilization: an array above
// 64 KiB accrues a strike where releasing it unwiped would have kept it, and
// is dropped after five such cycles. That is the intended trade for not
// leaving secrets in the pool.
//
// Two limits are worth stating. Wipe costs a memclr of the whole capacity,
// which is why it is opt-in rather than part of Release. And it reaches only
// from the current backing slice's start through its capacity: for an array
// bufpool allocated that is the whole array, but for one adopted through
// NewBuffer or SetBytes it excludes anything before the slice's start or
// beyond a capped capacity (arena[:n:n]) — and it cannot reach an array the
// buffer has already outgrown or otherwise replaced, since Grow and SetBytes
// abandon arrays without clearing them. Nothing the buffer itself wrote can
// lie outside that region; wipe before the buffer grows, not only at the end.
func (b *Buffer) Wipe() {
	clear(b.buf[:cap(b.buf)])
	b.readPos = 0
	b.buf = b.buf[:0]
}

// ReadAllBytes reads all remaining bytes from r. If r is a *Buffer, it returns
// the buffer's unread bytes directly without copying and advances the buffer
// to EOF; otherwise it falls back to io.ReadAll. The error is nil on success,
// mirroring io.ReadAll.
//
// Regardless of r's dynamic type, treat the returned slice as aliasing r's
// internal storage: it is only valid until r is next written to, reset,
// released or closed. Copy it if it must outlive r. For the *Buffer path the
// slice's capacity is limited to its length, so appending to it allocates
// rather than writing into the buffer; that is not guaranteed of the io.ReadAll
// fallback.
func ReadAllBytes(r io.Reader) ([]byte, error) {
	if bg, ok := r.(*Buffer); ok {
		result := bg.buf[bg.readPos:len(bg.buf):len(bg.buf)]
		bg.readPos = len(bg.buf)
		if result == nil {
			// Match the io.ReadAll fallback, which never returns a nil slice.
			result = []byte{}
		}
		return result, nil
	}
	return io.ReadAll(r)
}
