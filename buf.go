package bufpool

import (
	"io"
)

// Buffer is a byte buffer that may be attached to a Pool. It implements
// io.Reader, io.Writer, io.StringWriter, io.WriterTo and io.Closer. Writes
// append to the buffer; reads consume it from the front, tracked by an internal
// read position. The zero value is a usable, detached buffer.
//
// A Buffer must not be copied by value after first use, and must not be used
// after Recycle or Close.
type Buffer struct {
	poolStorage
	readPos int
	pool    *Pool
	storage *poolStorage // pooled object, reused on Recycle; nil if never pooled
}

// NewBuffer creates a new detached buffer whose initial contents are data. The
// slice becomes the buffer's backing array; it is not copied. NewBuffer(nil)
// creates an empty buffer.
func NewBuffer(data []byte) *Buffer {
	return &Buffer{poolStorage: poolStorage{buf: data}}
}

// Detach detaches the buffer from its pool. After Detach, Recycle and Close no
// longer recycle the buffer; it can be re-attached with Pool.Attach.
func (b *Buffer) Detach() {
	b.pool = nil
}

// SetBytes replaces the buffer's contents with p, which becomes the new backing
// array (it is not copied). The read position is left unchanged.
func (b *Buffer) SetBytes(p []byte) {
	if cap(p) > cap(b.buf) {
		b.strikes = 0
	}
	b.buf = p
}

// Bytes returns the unread portion of the buffer. The slice aliases the
// buffer's backing array and is only valid until the next mutating call.
func (b *Buffer) Bytes() []byte {
	return b.buf[b.readPos:]
}

// Recycle returns the buffer to its pool and resets it to the zero value, so
// the buffer must not be used after a successful Recycle. If the buffer is
// detached, Recycle is a no-op.
func (b *Buffer) Recycle() {
	if b.pool == nil {
		return
	}
	storage := b.storage
	if storage == nil {
		storage = new(poolStorage)
	}
	*storage = b.poolStorage
	b.pool.put(storage)
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

// Grow grows the buffer's capacity, if necessary, to guarantee space for
// another n bytes: after Grow(n), at least n bytes can be written without
// another allocation. Grow panics if n is negative.
func (b *Buffer) Grow(n int) {
	if n < 0 {
		panic("bufpool.Buffer.Grow: negative count")
	}
	if cap(b.buf)-len(b.buf) >= n {
		return
	}
	buf := make([]byte, len(b.buf), len(b.buf)+n)
	copy(buf, b.buf)
	b.strikes = 0
	b.buf = buf
}

// Close returns the buffer to its pool and always returns a nil error. It
// implements io.Closer and is equivalent to Recycle.
func (b *Buffer) Close() error {
	b.Recycle()
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
	b.readPos += nn
	if err == nil && nn < len(unread) {
		err = io.ErrShortWrite
	}
	return int64(nn), err
}

// Write appends p to the buffer, growing the backing array as needed. It always
// returns len(p) and a nil error. Write implements io.Writer.
func (b *Buffer) Write(p []byte) (int, error) {
	lp := len(p)
	if len(b.buf)+lp > cap(b.buf) {
		b.strikes = 0
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
// applying the same recycle heuristic the pool uses on Recycle: an oversized,
// repeatedly under-utilized backing array is dropped (replaced with a fresh nil
// buffer) instead of kept, so a single large use does not pin memory across
// resets. Unlike Recycle, the buffer stays usable and attached to its pool.
func (b *Buffer) Reset() {
	b.readPos = 0
	if b.recycle() {
		b.buf = b.buf[:0]
	} else {
		b.strikes = 0
		b.buf = nil
	}
}

// ReadAllBytes reads all remaining bytes from r. If r is a *Buffer, it returns
// the buffer's unread bytes directly without copying (aliasing the backing
// array) and advances the buffer to EOF; otherwise it falls back to io.ReadAll.
// The error is nil on success, mirroring io.ReadAll.
func ReadAllBytes(r io.Reader) ([]byte, error) {
	if bg, ok := r.(*Buffer); ok {
		result := bg.buf[bg.readPos:]
		bg.readPos = len(bg.buf)
		return result, nil
	}
	return io.ReadAll(r)
}
