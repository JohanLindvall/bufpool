package bufpool

import (
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_unit_New(t *testing.T) {
	p := New()
	assert.NotNil(t, p)
}

func Test_unit_Get(t *testing.T) {
	p := New()
	buf, byt := p.Get()
	assert.NotNil(t, buf)
	assert.Nil(t, byt)
}

func Test_unit_GetFrom(t *testing.T) {
	p := New()
	buf, byt := p.GetFrom([]byte("test"))
	assert.NotNil(t, buf)
	assert.Equal(t, []byte("test"), byt)
}

func Test_unit_Attach(t *testing.T) {
	p := New()
	buf := NewBuffer()
	err := p.Attach(buf)
	assert.Nil(t, err)
}

func Test_unit_Attach_Twice_Fail(t *testing.T) {
	p := New()
	buf := NewBuffer()
	_ = p.Attach(buf)
	err := p.Attach(buf)
	assert.NotNil(t, err)
}

func Test_unit_Attach_Attached_Fail(t *testing.T) {
	p := New()
	buf, _ := p.Get()
	err := p.Attach(buf)
	assert.NotNil(t, err)
}

func Test_unit_Pooled_Get(t *testing.T) {
	p := New()
	buf, _ := p.GetFrom([]byte("test"))
	buf.Recycle()
	buf, _ = p.Get()
	assert.Equal(t, 0, len(buf.buf))
	assert.Same(t, p, buf.pool)
	assert.Equal(t, 0, buf.strikes)
}

func Test_unit_Strikes(t *testing.T) {
	p := New()
	tests := []struct {
		name              string
		size, cap         int
		initial, expected int
	}{
		{"Small", 1, 2, 5, 0},
		{"Utilized", 70000, 100000, 5, 0},
		{"Large underutilized", 1, 100000, 2, 3},
		{"Large underutilized capped", 1, 100000, 4, 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf, _ := p.Get()
			buf.strikes = tt.initial
			buf.buf = make([]byte, tt.size, tt.cap)
			strikes := buf.Recycle()
			assert.Equal(t, tt.expected, strikes)
		})
	}
}

func Test_unit_Strikes_Read(t *testing.T) {
	p := New()
	buf, _ := p.Get()
	_, _ = buf.Write(make([]byte, 100000))
	_, _ = io.Copy(io.Discard, buf)
	buf.strikes = 999
	strikes := buf.Recycle()
	assert.Equal(t, 0, strikes)
}
