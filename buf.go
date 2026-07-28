package bufpool

import (
	"fmt"
	"io"
)

// minRead is the minimum spare capacity ReadFrom ensures before each Read
// from the source, mirroring bytes.MinRead.
const minRead = 512

// Buffer is a byte buffer that may be attached to a Pool. It implements
// io.Reader, io.Writer, io.StringWriter, io.ReaderFrom, io.WriterTo,
// io.Closer and fmt.Stringer. Writes append to the buffer; reads consume it
// from the front, tracked by an internal read position. The zero value is a
// usable, detached buffer.
//
// A Buffer must not be copied after first use (go vet reports such copies),
// and must not be used after Release or Close.
type Buffer struct {
	noCopy noCopy
	poolStorage
	readPos int
	pool    *Pool
	storage *poolStorage // pooled object, reused on Release; nil if never pooled
}

var _ interface {
	io.Reader
	io.Writer
	io.StringWriter
	io.ReaderFrom
	io.WriterTo
	io.Closer
	fmt.Stringer
} = (*Buffer)(nil)

// dropStorage clears the pooled storage's stale copy of a backing array the
// buffer is abandoning, so the array does not stay reachable through
// b.storage until the next Release.
func (b *Buffer) dropStorage() {
	if b.storage != nil {
		b.storage.buf = nil
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
func (b *Buffer) SetBytes(p []byte) {
	b.strikes = 0
	b.readPos = 0
	b.dropStorage()
	b.buf = p
}

// Bytes returns the unread portion of the buffer. The slice aliases the
// buffer's backing array and is only valid until the next mutating call;
// Release, Close and Reset invalidate it. Copy the bytes (or use String) if
// they must outlive the buffer.
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
// Releasing invalidates all slices previously returned by Bytes or
// ReadAllBytes: the backing array re-enters the pool and the next Get may
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
// another allocation. Grow panics if n is negative or if the buffer would
// grow beyond the maximum slice length.
func (b *Buffer) Grow(n int) {
	if n < 0 {
		panic("bufpool.Buffer.Grow: negative count")
	}
	if cap(b.buf)-len(b.buf) >= n {
		return
	}
	buf := makeBuf(len(b.buf), len(b.buf)+n)
	copy(buf, b.buf)
	b.strikes = 0
	b.dropStorage()
	b.buf = buf
}

// makeBuf allocates a backing array, converting the runtime's allocation-size
// panic (int overflow or beyond the maximum slice length) into the documented
// Grow panic, mirroring bytes.growSlice.
func makeBuf(length, capacity int) (buf []byte) {
	defer func() {
		if recover() != nil {
			panic("bufpool.Buffer.Grow: too large")
		}
	}()
	return make([]byte, length, capacity)
}

// Close returns the buffer to its pool and always returns a nil error. It
// implements io.Closer and is equivalent to Release, so a pooled *Buffer can
// be handed off as an io.ReadCloser and is returned to the pool at the release site
// without a pool reference in scope.
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
// encountered during the read. ReadFrom implements io.ReaderFrom, so io.Copy
// into a Buffer needs no intermediate copy buffer.
func (b *Buffer) ReadFrom(r io.Reader) (int64, error) {
	var total int64
	for {
		if cap(b.buf)-len(b.buf) < minRead {
			// Grow geometrically so repeated fills stay amortized O(n).
			b.Grow(max(minRead, cap(b.buf)))
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
// returns len(p) and a nil error. Write implements io.Writer.
func (b *Buffer) Write(p []byte) (int, error) {
	lp := len(p)
	if len(b.buf)+lp > cap(b.buf) {
		b.strikes = 0
		b.dropStorage() // append is about to abandon the current array
	}

	b.buf = append(b.buf, p...)
	return lp, nil
}

// WriteString appends s to the buffer without copying it into a temporary
// []byte first. It always returns len(s) and a nil error. WriteString
// implements io.StringWriter.
func (b *Buffer) WriteString(s string) (int, error) {
	if len(b.buf)+len(s) > cap(b.buf) {
		b.strikes = 0
		b.dropStorage() // append is about to abandon the current array
	}

	b.buf = append(b.buf, s...)
	return len(s), nil
}

// Rewind resets the read position to zero so the buffer's full contents can be
// read again. It does not modify the contents.
func (b *Buffer) Rewind() {
	b.readPos = 0
}

// Reset rewinds the read position and truncates the buffer for in-place reuse,
// applying the same keep-or-discard heuristic the pool uses on Release: an oversized,
// repeatedly under-utilized backing array is dropped (replaced with a fresh nil
// buffer) instead of kept, so a single large use does not pin memory across
// resets. Unlike Release, the buffer stays usable and attached to its pool.
func (b *Buffer) Reset() {
	b.readPos = 0
	if b.keep() {
		b.buf = b.buf[:0]
	} else {
		b.strikes = 0
		b.buf = nil
		// Drop the pooled copy of the slice header too, so the discarded
		// array is not kept reachable until the next Release.
		b.dropStorage()
	}
}

// ReadAllBytes reads all remaining bytes from r. If r is a *Buffer, it returns
// the buffer's unread bytes directly without copying and advances the buffer
// to EOF; otherwise it falls back to io.ReadAll. The error is nil on success,
// mirroring io.ReadAll.
//
// Regardless of r's dynamic type, treat the returned slice as aliasing r's
// internal storage: it is only valid until r is next written to, reset,
// released or closed. Copy it if it must outlive r.
func ReadAllBytes(r io.Reader) ([]byte, error) {
	if bg, ok := r.(*Buffer); ok {
		result := bg.buf[bg.readPos:]
		bg.readPos = len(bg.buf)
		return result, nil
	}
	return io.ReadAll(r)
}
