package bufpool

import (
	"bytes"
	"errors"
	"io"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// getFilled returns a pooled buffer pre-filled with data.
func getFilled(p *Pool, data string) *Buffer {
	buf := p.Get()
	_, _ = buf.WriteString(data)
	return buf
}

func Test_unit_SetBytes(t *testing.T) {
	p := new(Pool)
	buf := p.Get()
	buf.SetBytes([]byte("test"))
	assert.Equal(t, []byte("test"), buf.buf)
	assert.Equal(t, 0, buf.readPos)
}

func Test_unit_SetBytes_AfterRead_Rewinds(t *testing.T) {
	// Regression: SetBytes with a slice shorter than the consumed prefix used
	// to leave readPos beyond the new length, producing a negative Len and a
	// slice-bounds panic on the next Read/Bytes. SetBytes now rewinds.
	buf := NewBuffer(nil)
	_, _ = buf.WriteString("hello")
	_, _ = buf.Read(make([]byte, 4))
	buf.SetBytes([]byte("ab"))
	assert.Equal(t, 2, buf.Len())
	assert.Equal(t, []byte("ab"), buf.Bytes())
	data, err := io.ReadAll(buf)
	assert.Nil(t, err)
	assert.Equal(t, []byte("ab"), data)
}

// pooledBuffer builds the state Pool.Get produces after a backing array has
// round-tripped through the pool, without going through sync.Pool itself,
// whose caching is deliberately nondeterministic under the race detector.
func pooledBuffer(size int) *Buffer {
	storage := &poolStorage{buf: make([]byte, size)}
	b := &Buffer{poolStorage: *storage, storage: storage, pool: new(Pool)}
	b.buf = b.buf[:0]
	return b
}

func Test_unit_SetBytes_DropsPooledStorageRef(t *testing.T) {
	// Regression: SetBytes abandons the current backing array; the pooled
	// storage's copy of the slice header must not keep it reachable for the
	// handle's remaining lifetime.
	buf := pooledBuffer(1 << 20) // carries the 1 MiB array in buf.storage
	buf.SetBytes(make([]byte, 16))
	assert.Nil(t, buf.storage.buf, "abandoned array must not stay reachable via the pooled storage")
	assert.NotPanics(t, buf.Release) // and releasing the replacement still works
}

func Test_unit_Grow_DropsPooledStorageRef(t *testing.T) {
	// Same as above for Grow's reallocation path.
	buf := pooledBuffer(1 << 16)
	buf.Grow(1 << 20)
	assert.Nil(t, buf.storage.buf)
}

func Test_unit_Write_DropsPooledStorageRef(t *testing.T) {
	// Same as above for Write/WriteString growing past the current capacity.
	buf := pooledBuffer(1 << 16)
	_, _ = buf.Write(make([]byte, 1<<16+1)) // forces append to reallocate
	assert.Nil(t, buf.storage.buf)
	buf2 := pooledBuffer(0)
	_, _ = buf2.WriteString("x") // zero-cap append also reallocates
	assert.Nil(t, buf2.storage.buf)
}

func Test_unit_SetBytes_ClearsStrikes(t *testing.T) {
	// A replacement backing array must not inherit the old array's strikes.
	buf := &Buffer{poolStorage: poolStorage{buf: make([]byte, 0, 1<<17), strikes: 4}}
	buf.SetBytes(make([]byte, 1000, 1<<17))
	assert.Equal(t, 0, buf.strikes)
}

func Test_unit_Bytes(t *testing.T) {
	buf := &Buffer{poolStorage: poolStorage{buf: []byte("test")}}
	assert.Equal(t, []byte("test"), buf.Bytes())
}

func Test_unit_Bytes_Read(t *testing.T) {
	buf := &Buffer{poolStorage: poolStorage{buf: []byte("test")}, readPos: 1}
	assert.Equal(t, []byte("est"), buf.Bytes())
}

func Test_unit_String(t *testing.T) {
	buf := NewBuffer([]byte("test"))
	_, _ = buf.Read(make([]byte, 1))
	s := buf.String()
	assert.Equal(t, "est", s)
	// String copies: mutating or releasing the buffer must not affect it.
	_, _ = buf.WriteString("!!!")
	assert.Equal(t, "est", s)
}

func Test_unit_String_Nil(t *testing.T) {
	var buf *Buffer
	assert.Equal(t, "<nil>", buf.String())
}

func Test_unit_Detach(t *testing.T) {
	p := new(Pool)
	buf := p.Get()
	buf.Detach()
	assert.Nil(t, buf.pool)
}

func Test_unit_Write(t *testing.T) {
	p := new(Pool)
	buf := p.Get()
	n, err := buf.Write([]byte("test"))
	assert.Nil(t, err)
	assert.Equal(t, 4, n)
	assert.Equal(t, []byte("test"), buf.buf)
}

type nullWriter struct {
	n   int
	err error
}

func (n *nullWriter) Write(p []byte) (int, error) {
	return n.n, n.err
}

func Test_unit_WriteTo(t *testing.T) {
	p := new(Pool)
	buf := getFilled(p, "test")
	dest := bytes.NewBuffer(nil)
	n, err := buf.WriteTo(dest)
	assert.Nil(t, err)
	assert.Equal(t, int64(4), n)
	assert.Equal(t, []byte("test"), dest.Bytes())
}

func Test_unit_WriteTo_ShortWrite(t *testing.T) {
	p := new(Pool)
	buf := getFilled(p, "test")
	n, err := buf.WriteTo(&nullWriter{n: 1, err: nil})
	assert.ErrorIs(t, err, io.ErrShortWrite)
	assert.Equal(t, int64(1), n)
	// The accepted byte is consumed; the rest remains readable.
	dest := bytes.NewBuffer(nil)
	n, err = buf.WriteTo(dest)
	assert.Nil(t, err)
	assert.Equal(t, int64(3), n)
	assert.Equal(t, []byte("est"), dest.Bytes())
}

func Test_unit_WriteTo_Error(t *testing.T) {
	p := new(Pool)
	buf := getFilled(p, "test")
	// A writer error is propagated, and the accepted bytes are still consumed.
	wantErr := errors.New("boom")
	n, err := buf.WriteTo(&nullWriter{n: 1, err: wantErr})
	assert.ErrorIs(t, err, wantErr)
	assert.Equal(t, int64(1), n)
	assert.Equal(t, []byte("est"), buf.Bytes())
}

func Test_unit_WriteTo_Empty(t *testing.T) {
	p := new(Pool)
	buf := getFilled(p, "x")
	_, _ = io.Copy(io.Discard, buf)
	// A drained buffer must not call the writer at all.
	n, err := buf.WriteTo(&nullWriter{err: errors.New("should not be called")})
	assert.Nil(t, err)
	assert.Equal(t, int64(0), n)
}

func Test_unit_WriteTo_InvalidCount_Panics(t *testing.T) {
	// A writer violating the io.Writer contract must fail loudly at the call
	// site (like bytes.Buffer), not corrupt the read position.
	buf := NewBuffer([]byte("abcd"))
	assert.PanicsWithValue(t, "bufpool.Buffer.WriteTo: invalid Write count", func() {
		_, _ = buf.WriteTo(&nullWriter{n: 10})
	})
	buf2 := NewBuffer([]byte("abcd"))
	assert.PanicsWithValue(t, "bufpool.Buffer.WriteTo: invalid Write count", func() {
		_, _ = buf2.WriteTo(&nullWriter{n: -1})
	})
}

func Test_unit_ReadFrom(t *testing.T) {
	p := new(Pool)
	buf := p.Get()
	defer buf.Release()
	data := strings.Repeat("x", 5000) // forces several growth rounds
	n, err := buf.ReadFrom(strings.NewReader(data))
	assert.Nil(t, err)
	assert.Equal(t, int64(5000), n)
	assert.Equal(t, 5000, buf.Len())
	assert.Equal(t, []byte(data), buf.Bytes())
}

func Test_unit_ReadFrom_Appends(t *testing.T) {
	// ReadFrom appends after existing content and preserves the read position.
	buf := NewBuffer(nil)
	_, _ = buf.WriteString("abc")
	_, _ = buf.Read(make([]byte, 1))
	n, err := buf.ReadFrom(strings.NewReader("def"))
	assert.Nil(t, err)
	assert.Equal(t, int64(3), n)
	assert.Equal(t, []byte("bcdef"), buf.Bytes())
}

type errReader struct {
	data []byte
	err  error
}

func (e *errReader) Read(p []byte) (int, error) {
	n := copy(p, e.data)
	e.data = e.data[n:]
	if len(e.data) == 0 {
		return n, e.err
	}
	return n, nil
}

func Test_unit_ReadFrom_Error(t *testing.T) {
	// Errors other than io.EOF are returned along with the bytes read so far.
	buf := NewBuffer(nil)
	wantErr := errors.New("boom")
	n, err := buf.ReadFrom(&errReader{data: []byte("abc"), err: wantErr})
	assert.ErrorIs(t, err, wantErr)
	assert.Equal(t, int64(3), n)
	assert.Equal(t, []byte("abc"), buf.Bytes())
}

type negReader struct{}

func (negReader) Read([]byte) (int, error) { return -1, nil }

func Test_unit_ReadFrom_NegativeCount_Panics(t *testing.T) {
	buf := NewBuffer(nil)
	assert.PanicsWithValue(t, "bufpool.Buffer.ReadFrom: reader returned negative count from Read", func() {
		_, _ = buf.ReadFrom(negReader{})
	})
}

func Test_unit_ReadFrom_Copy(t *testing.T) {
	// io.Copy from a plain reader must take the ReaderFrom fast path and
	// deliver everything.
	buf := NewBuffer(nil)
	n, err := io.Copy(buf, iotestOneByteReader{strings.NewReader("test")})
	assert.Nil(t, err)
	assert.Equal(t, int64(4), n)
	assert.Equal(t, []byte("test"), buf.Bytes())
}

// iotestOneByteReader delivers one byte per Read, exercising ReadFrom's loop.
type iotestOneByteReader struct{ r io.Reader }

func (r iotestOneByteReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return r.r.Read(p)
	}
	return r.r.Read(p[:1])
}

func Test_unit_Len(t *testing.T) {
	p := new(Pool)
	buf := getFilled(p, "test")
	assert.Equal(t, 4, buf.Len())
	assert.Equal(t, 4, buf.Size())
	// Len counts unread bytes only; Size counts everything written.
	_, _ = buf.Read(make([]byte, 3))
	assert.Equal(t, 1, buf.Len())
	assert.Equal(t, 4, buf.Size())
}

func Test_unit_Len_Pooled(t *testing.T) {
	p := new(Pool)
	buf := getFilled(p, "test")
	buf.Release()
	assert.Equal(t, 0, buf.Len())
	assert.Equal(t, 0, buf.Size())
}

func Test_unit_Cap(t *testing.T) {
	buf := NewBuffer(make([]byte, 2, 128))
	assert.Equal(t, 128, buf.Cap())
	// Cap includes the consumed portion; reading must not change it.
	_, _ = buf.Read(make([]byte, 1))
	assert.Equal(t, 128, buf.Cap())
}

func Test_unit_Grow(t *testing.T) {
	buf := NewBuffer(nil)
	buf.Grow(100)
	assert.Equal(t, 0, buf.Len())
	assert.GreaterOrEqual(t, buf.Cap(), 100)
	// Writing within the grown capacity must not reallocate.
	c := buf.Cap()
	_, _ = buf.Write(make([]byte, 100))
	assert.Equal(t, c, buf.Cap())
}

func Test_unit_Grow_Negative(t *testing.T) {
	buf := NewBuffer(nil)
	assert.Panics(t, func() { buf.Grow(-1) })
}

func Test_unit_Grow_TooLarge(t *testing.T) {
	// Growing beyond what a slice can hold must panic with the documented
	// message, not a generic makeslice runtime error — both when len+n
	// overflows int and when it merely exceeds the maximum allocation.
	buf := NewBuffer([]byte("x"))
	assert.PanicsWithValue(t, "bufpool.Buffer: too large", func() {
		buf.Grow(math.MaxInt) // 1+MaxInt overflows
	})
	buf2 := NewBuffer(nil)
	assert.PanicsWithValue(t, "bufpool.Buffer: too large", func() {
		buf2.Grow(math.MaxInt) // no overflow, but beyond the max slice length
	})
}

func Test_unit_Grow_Amortized(t *testing.T) {
	// Grow over-allocates (at least doubling), so a Grow+Write loop causes
	// O(log n) reallocations, not one per iteration.
	buf := NewBuffer(nil)
	reallocs := 0
	chunk := make([]byte, 100)
	for i := 0; i < 1000; i++ {
		c := buf.Cap()
		buf.Grow(len(chunk))
		if buf.Cap() != c {
			reallocs++
		}
		_, _ = buf.Write(chunk)
	}
	assert.Equal(t, 100_000, buf.Len())
	assert.Less(t, reallocs, 20) // ~log2(100000/64) if doubling holds
}

func Test_unit_Grow_SufficientCapacity(t *testing.T) {
	// Growing within existing spare capacity is a no-op that keeps the
	// backing array.
	buf := NewBuffer(make([]byte, 0, 128))
	c := buf.Cap()
	buf.Grow(100)
	assert.Equal(t, c, buf.Cap())
}

func Test_unit_Grow_PreservesContents(t *testing.T) {
	buf := NewBuffer(nil)
	_, _ = buf.WriteString("abc")
	_, _ = buf.Read(make([]byte, 1))
	buf.Grow(1 << 10)
	assert.Equal(t, []byte("bc"), buf.Bytes()) // contents and read position survive
	assert.Equal(t, 3, buf.Size())
}

func Test_unit_Release_Detached(t *testing.T) {
	// Release on a detached buffer is a no-op; the buffer stays usable.
	buf := NewBuffer(nil)
	_, _ = buf.WriteString("test")
	buf.Release()
	assert.Equal(t, []byte("test"), buf.Bytes())
}

func Test_unit_Release_Twice(t *testing.T) {
	// A second Release is a safe no-op because the first one detaches.
	p := new(Pool)
	buf := p.Get()
	buf.Release()
	assert.NotPanics(t, func() { buf.Release() })
}

func Test_unit_Close(t *testing.T) {
	p := new(Pool)
	buf := p.Get()
	err := buf.Close()
	assert.Nil(t, err)
	assert.Nil(t, buf.pool)
}

func Test_unit_Read(t *testing.T) {
	p := new(Pool)
	buf := getFilled(p, "test")
	data, err := io.ReadAll(buf)
	assert.Nil(t, err)
	assert.Equal(t, []byte("test"), data)
}

func Test_unit_ReadShort(t *testing.T) {
	p := new(Pool)
	buf := getFilled(p, "test")
	data := []byte{0, 0}
	n, err := buf.Read(data)
	assert.Nil(t, err)
	assert.Equal(t, 2, n)
	assert.Equal(t, []byte("te"), data)
	assert.Equal(t, 2, buf.readPos)
	assert.Equal(t, []byte("test"), buf.buf)
}

func Test_unit_ReadShortTwice(t *testing.T) {
	p := new(Pool)
	buf := getFilled(p, "test")
	data := []byte{0, 0}
	_, _ = buf.Read(data)
	n, err := buf.Read(data)
	assert.Nil(t, err)
	assert.Equal(t, 2, n)
	assert.Equal(t, []byte("st"), data)
	assert.Equal(t, 4, buf.readPos)
	assert.Equal(t, []byte("test"), buf.buf)
}

func Test_unit_ReadShortThreeTimes(t *testing.T) {
	p := new(Pool)
	buf := getFilled(p, "test")
	data := []byte{0, 0}
	_, _ = buf.Read(data)
	_, _ = buf.Read(data)
	n, err := buf.Read(data)
	assert.Same(t, io.EOF, err)
	assert.Equal(t, 0, n)
	assert.Equal(t, []byte("test"), buf.buf)
}

func Test_unit_ReadAllBytes_Buffer(t *testing.T) {
	buf := NewBuffer(nil)
	_, _ = buf.Write([]byte("test"))
	data, err := ReadAllBytes(buf)
	assert.Nil(t, err)
	assert.Equal(t, []byte("test"), data)
}

func Test_unit_ReadAllBytesTwice_Buffer(t *testing.T) {
	buf := NewBuffer(nil)
	_, _ = buf.Write([]byte("test"))
	_, _ = ReadAllBytes(buf)
	data, err := ReadAllBytes(buf)
	assert.Nil(t, err)
	assert.Equal(t, 0, len(data))
}

func Test_unit_ReadAllBytes_Generic(t *testing.T) {
	buf := bytes.NewBuffer(nil)
	buf.Write([]byte("test"))
	data, err := ReadAllBytes(buf)
	assert.Nil(t, err)
	assert.Equal(t, []byte("test"), data)
}

func Test_unit_ReadAllBytesTwice_Generic(t *testing.T) {
	buf := bytes.NewBuffer(nil)
	buf.Write([]byte("test"))
	_, _ = ReadAllBytes(buf)
	data, err := ReadAllBytes(buf)
	assert.Nil(t, err)
	assert.Equal(t, 0, len(data))
}

func Test_unit_Write_CompactsDrainedBuffer(t *testing.T) {
	// A write to a fully-consumed buffer reuses the backing array from the
	// start instead of appending behind the consumed prefix, so FIFO-style
	// use (write, drain, repeat) stays at working-set size.
	buf := NewBuffer(nil)
	p := make([]byte, 1024)
	for i := 0; i < 1000; i++ {
		_, _ = buf.Write(p)
		_, err := io.ReadFull(buf, p)
		assert.Nil(t, err)
	}
	assert.Equal(t, 0, buf.Len())
	assert.Less(t, buf.Cap(), 1<<16, "drained-and-refilled buffer must not grow with total bytes written")
}

func Test_unit_ReadFrom_CompactsDrainedBuffer(t *testing.T) {
	// ReadFrom compacts a fully-consumed buffer before filling, like Write.
	buf := NewBuffer(nil)
	_, _ = buf.WriteString("abc")
	_, _ = io.Copy(io.Discard, buf)
	n, err := buf.ReadFrom(strings.NewReader("def"))
	assert.Nil(t, err)
	assert.Equal(t, int64(3), n)
	assert.Equal(t, "def", buf.String())
	assert.Equal(t, 3, buf.Size())
}

func Test_unit_Rewind_AfterPartialRead(t *testing.T) {
	// Compaction happens only when the buffer is fully consumed; after a
	// partial read, writes append and Rewind still replays everything.
	buf := NewBuffer(nil)
	_, _ = buf.WriteString("abc")
	_, _ = buf.Read(make([]byte, 2))
	_, _ = buf.WriteString("def")
	buf.Rewind()
	assert.Equal(t, "abcdef", buf.String())
}

func Test_unit_Rewind_AfterCompactingWrite(t *testing.T) {
	// A write to a fully-drained buffer compacts it: the consumed bytes are
	// discarded and Rewind replays only what was written since.
	buf := NewBuffer(nil)
	_, _ = buf.WriteString("abc")
	_, _ = io.Copy(io.Discard, buf)
	_, _ = buf.WriteString("def")
	buf.Rewind()
	assert.Equal(t, "def", buf.String())
	assert.Equal(t, 3, buf.Size())
}

func Test_unit_WriteByte_ReadByte(t *testing.T) {
	p := new(Pool)
	buf := p.Get()
	defer buf.Release()
	assert.Nil(t, buf.WriteByte('a'))
	assert.Nil(t, buf.WriteByte('b'))
	c, err := buf.ReadByte()
	assert.Nil(t, err)
	assert.Equal(t, byte('a'), c)
	c, err = buf.ReadByte()
	assert.Nil(t, err)
	assert.Equal(t, byte('b'), c)
	_, err = buf.ReadByte()
	assert.Same(t, io.EOF, err)
}

func Test_unit_WriteByte_DropsPooledStorageRef(t *testing.T) {
	// WriteByte growing past the current capacity abandons the array like
	// Write does, dropping the pooled storage's stale slice header.
	buf := pooledBuffer(4)
	for i := 0; i < 5; i++ {
		assert.Nil(t, buf.WriteByte('x'))
	}
	assert.Equal(t, "xxxxx", buf.String())
	assert.Nil(t, buf.storage.buf)
}

func Test_unit_Next(t *testing.T) {
	buf := NewBuffer([]byte("abcdef"))
	assert.Equal(t, []byte("abc"), buf.Next(3))
	assert.Equal(t, []byte("de"), buf.Next(2))
	assert.Equal(t, []byte("f"), buf.Next(10)) // clamped to the unread remainder
	assert.Equal(t, 0, len(buf.Next(1)))
	assert.Equal(t, 0, buf.Len())
}

func Test_unit_Next_Negative_Panics(t *testing.T) {
	buf := NewBuffer([]byte("abc"))
	assert.Panics(t, func() { buf.Next(-1) })
}

func Test_unit_ReadAllBytes_EmptyNonNil(t *testing.T) {
	// Matches the io.ReadAll fallback: an empty result is a non-nil empty
	// slice regardless of the reader's dynamic type.
	data, err := ReadAllBytes(NewBuffer(nil))
	assert.Nil(t, err)
	assert.NotNil(t, data)
	assert.Equal(t, 0, len(data))
}

func Test_unit_Rewind(t *testing.T) {
	p := new(Pool)
	buf := getFilled(p, "test")
	_, _ = io.Copy(io.Discard, buf)
	buf.Rewind()
	data, err := ReadAllBytes(buf)
	assert.Nil(t, err)
	assert.Equal(t, []byte("test"), data)
}

func Test_unit_WriteString(t *testing.T) {
	buf := &Buffer{}
	n, err := buf.WriteString("ab")
	assert.NoError(t, err)
	assert.Equal(t, 2, n)
	_, _ = buf.WriteString("c")
	assert.Equal(t, []byte("abc"), buf.Bytes())
}

func Test_unit_Reset(t *testing.T) {
	// A small buffer keeps its backing array; Reset truncates the length, rewinds
	// the read position and clears the strike counter, leaving it reusable.
	buf := &Buffer{poolStorage: poolStorage{buf: []byte("hello")}, readPos: 3}
	buf.Reset()
	assert.Equal(t, 0, buf.readPos)
	assert.Equal(t, 0, len(buf.buf))
	assert.Equal(t, 0, buf.strikes)
	_, _ = buf.WriteString("hi")
	assert.Equal(t, []byte("hi"), buf.Bytes())
}

func Test_unit_Reset_DiscardsOversizedUnderUtilized(t *testing.T) {
	// A large, under-utilized buffer is kept for up to four strikes, then its
	// oversized backing array is dropped (matching the pool's put heuristic).
	buf := &Buffer{poolStorage: poolStorage{buf: make([]byte, 0, 1<<17)}}
	for i := 0; i < 4; i++ {
		buf.Reset()
		assert.NotNil(t, buf.buf, "kept on strike %d", i+1)
		assert.Equal(t, i+1, buf.strikes)
	}
	buf.Reset() // fifth consecutive under-utilized reset discards the backing
	assert.Nil(t, buf.buf)
	assert.Equal(t, 0, buf.strikes)
}

func Test_unit_Reset_Discard_ClearsPooledStorage(t *testing.T) {
	// Regression: the discard path used to nil only the Buffer's embedded
	// slice, leaving the oversized array pinned via the pooled storage's copy
	// of the slice header until the next Release.
	buf := pooledBuffer(1 << 17) // 128 KiB array carried in buf.storage
	assert.Equal(t, 1<<17, buf.Cap())
	for i := 0; i < 5; i++ {
		buf.Reset()
	}
	assert.Nil(t, buf.buf)
	assert.NotNil(t, buf.storage)
	assert.Nil(t, buf.storage.buf, "discarded array must not stay reachable via the pooled storage")
}

func Test_unit_Reset_KeepsWellUtilized(t *testing.T) {
	// A large but well-utilized buffer is kept and its strike count cleared.
	buf := &Buffer{poolStorage: poolStorage{buf: make([]byte, 1<<17), strikes: 3}}
	buf.Reset()
	assert.Equal(t, 0, buf.strikes)
	assert.Equal(t, 0, len(buf.buf))
	assert.Equal(t, 1<<17, buf.Cap())
}
