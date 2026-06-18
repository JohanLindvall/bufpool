package bufpool

import (
	"bytes"
	"io"
)

// Buffer is a byte buffer that may be attached to a Pool. It implements
// io.Reader, io.Writer, io.StringWriter, io.WriterTo and io.Closer. Writes
// append to the buffer; reads consume it from the front, tracked by an internal
// read position. The zero value is a usable, detached buffer.
type Buffer struct {
	poolStorage
	readPos int
	pool    *Pool
}

// NewBuffer creates a new detached buffer whose initial contents are in. The
// passed bytes become the buffer's backing array; they are not copied.
func NewBuffer(in ...byte) *Buffer {
	return &Buffer{poolStorage: poolStorage{buf: in}}
}

// Detach detaches the buffer from its pool. After Detach, Return and Close no
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

// Return recycles the buffer into its pool and resets it to the zero value, so
// the buffer must not be used after a successful Return. If the buffer is
// detached, Return is a no-op and returns 0. The returned int is the buffer's
// resulting strike count, the number of consecutive times its oversized backing
// array has been kept despite being under-utilized (see the recycle heuristic);
// it is mainly useful for tests and tuning.
func (b *Buffer) Return() int {
	strikes := 0
	if b.pool != nil {
		copy := b.poolStorage
		strikes = b.pool.put(&copy)
		*b = Buffer{}
	}
	return strikes
}

// Len returns the total length of the buffer, including any portion already
// consumed by Read. Use len(b.Bytes()) for the number of unread bytes.
func (b *Buffer) Len() int {
	return len(b.buf)
}

// Close returns the buffer to its pool and always returns a nil error. It
// implements io.Closer and is equivalent to Return.
func (b *Buffer) Close() error {
	b.Return()
	return nil
}

// Read consumes up to len(p) unread bytes into p, advancing the read position.
// It returns io.EOF once the buffer is fully consumed. Read implements
// io.Reader.
func (b *Buffer) Read(p []byte) (n int, err error) {
	n = len(b.buf) - b.readPos
	if n == 0 {
		return 0, io.EOF
	}
	if n > len(p) {
		n = len(p)
	}
	copy(p, b.buf[b.readPos:n+b.readPos])
	b.readPos += n
	return n, nil
}

// WriteTo writes the unread portion of the buffer to w, advancing the read
// position by the number of bytes accepted by w. WriteTo implements
// io.WriterTo.
func (b *Buffer) WriteTo(w io.Writer) (n int64, err error) {
	var nn int
	nn, err = w.Write(b.buf[b.readPos:])
	b.readPos += nn
	n = int64(nn)
	return
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
// applying the same recycle heuristic the pool uses on Return: an oversized,
// repeatedly under-utilized backing array is dropped (replaced with a fresh nil
// buffer) instead of kept, so a single large use does not pin memory across
// resets. Unlike Return, the buffer stays usable and attached to its pool.
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
// array) and advances the buffer to EOF; otherwise it falls back to copying via
// io.Copy. The error is nil on success, mirroring io.ReadAll.
func ReadAllBytes(r io.Reader) ([]byte, error) {
	if bg, ok := r.(*Buffer); ok {
		result := bg.buf[bg.readPos:]
		bg.readPos = len(bg.buf)
		return result, nil
	}
	data := bytes.NewBuffer(nil)
	_, err := io.Copy(data, r)
	return data.Bytes(), err
}
