package bufpool

import (
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_unit_SetBytes(t *testing.T) {
	p := New()
	buf, _ := p.Get()
	buf.SetBytes([]byte("test"))
	assert.Equal(t, []byte("test"), buf.buf)
}

func Test_unit_Bytes(t *testing.T) {
	buf := &Buffer{poolStorage: poolStorage{buf: []byte("test")}}
	assert.Equal(t, []byte("test"), buf.Bytes())
}

func Test_unit_Bytes_Read(t *testing.T) {
	buf := &Buffer{poolStorage: poolStorage{buf: []byte("test")}, readPos: 1}
	assert.Equal(t, []byte("est"), buf.Bytes())
}

func Test_unit_Detach(t *testing.T) {
	p := New()
	buf, _ := p.Get()
	buf.Detach()
	assert.Nil(t, buf.pool)
}

func Test_unit_Write(t *testing.T) {
	p := New()
	buf, _ := p.Get()
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
	p := New()
	buf, _ := p.Get()
	_, _ = buf.Write([]byte("test"))
	dest := bytes.NewBuffer(nil)
	n, err := buf.WriteTo(dest)
	assert.Nil(t, err)
	assert.Equal(t, int64(4), n)
	assert.Equal(t, []byte("test"), dest.Bytes())
}

func Test_unit_WriteTo_Skip(t *testing.T) {
	p := New()
	buf, _ := p.Get()
	_, _ = buf.Write([]byte("test"))
	_, _ = buf.WriteTo(&nullWriter{n: 1, err: nil})
	dest := bytes.NewBuffer(nil)
	n, err := buf.WriteTo(dest)
	assert.Nil(t, err)
	assert.Equal(t, int64(3), n)
	assert.Equal(t, []byte("est"), dest.Bytes())
}

func Test_unit_Len(t *testing.T) {
	p := New()
	buf, _ := p.Get()
	_, _ = buf.Write([]byte("test"))
	n := buf.Len()
	assert.Equal(t, 4, n)
}

func Test_unit_Len_Pooled(t *testing.T) {
	p := New()
	buf, _ := p.Get()
	_, _ = buf.Write([]byte("test"))
	buf.Recycle()
	n := buf.Len()
	assert.Equal(t, 0, n)
}

func Test_unit_Close(t *testing.T) {
	p := New()
	buf, _ := p.Get()
	err := buf.Close()
	assert.Nil(t, err)
	assert.Nil(t, buf.pool)
}

func Test_unit_Read(t *testing.T) {
	p := New()
	buf, _ := p.GetFrom([]byte("test"))
	data, err := io.ReadAll(buf)
	assert.Nil(t, err)
	assert.Equal(t, []byte("test"), data)
}

func Test_unit_ReadShort(t *testing.T) {
	p := New()
	buf, _ := p.GetFrom([]byte("test"))
	data := []byte{0, 0}
	n, err := buf.Read(data)
	assert.Nil(t, err)
	assert.Equal(t, 2, n)
	assert.Equal(t, []byte("te"), data)
	assert.Equal(t, 2, buf.readPos)
	assert.Equal(t, []byte("test"), buf.buf)
}

func Test_unit_ReadShortTwice(t *testing.T) {
	p := New()
	buf, _ := p.GetFrom([]byte("test"))
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
	p := New()
	buf, _ := p.GetFrom([]byte("test"))
	data := []byte{0, 0}
	_, _ = buf.Read(data)
	_, _ = buf.Read(data)
	n, err := buf.Read(data)
	assert.Same(t, io.EOF, err)
	assert.Equal(t, 0, n)
	assert.Equal(t, []byte("test"), buf.buf)
}

func Test_unit_ReadAllBytes_Buffer(t *testing.T) {
	buf := NewBuffer()
	_, _ = buf.Write([]byte("test"))
	data, err := ReadAllBytes(buf)
	assert.Nil(t, err)
	assert.Equal(t, []byte("test"), data)
}

func Test_unit_ReadAllBytesTwice_Buffer(t *testing.T) {
	buf := NewBuffer()
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

func Test_unit_Rewind(t *testing.T) {
	p := New()
	buf, _ := p.Get()
	_, _ = buf.Write([]byte("test"))
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

func Test_unit_Reset_KeepsWellUtilized(t *testing.T) {
	// A large but well-utilized buffer is kept and its strike count cleared.
	buf := &Buffer{poolStorage: poolStorage{buf: make([]byte, 1<<17), strikes: 3}}
	buf.Reset()
	assert.Equal(t, 0, buf.strikes)
	assert.Equal(t, 0, len(buf.buf))
	assert.Equal(t, 1<<17, cap(buf.buf))
}
