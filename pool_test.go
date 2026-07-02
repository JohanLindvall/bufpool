package bufpool

import (
	"io"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_unit_New(t *testing.T) {
	p := New()
	assert.NotNil(t, p)
}

func Test_unit_Get(t *testing.T) {
	p := New()
	buf := p.Get()
	assert.NotNil(t, buf)
	assert.Equal(t, 0, buf.Len())
}

func Test_unit_GetFrom(t *testing.T) {
	p := New()
	buf := p.GetFrom([]byte("test"))
	assert.NotNil(t, buf)
	assert.Equal(t, []byte("test"), buf.Bytes())
}

func Test_unit_GetFrom_Copies(t *testing.T) {
	// GetFrom must copy: mutating the source afterwards must not affect the buffer.
	p := New()
	src := []byte("test")
	buf := p.GetFrom(src)
	src[0] = 'X'
	assert.Equal(t, []byte("test"), buf.Bytes())
}

func Test_unit_Attach(t *testing.T) {
	p := New()
	buf := NewBuffer(nil)
	err := p.Attach(buf)
	assert.Nil(t, err)
}

func Test_unit_Attach_Twice_Fail(t *testing.T) {
	p := New()
	buf := NewBuffer(nil)
	_ = p.Attach(buf)
	err := p.Attach(buf)
	assert.ErrorIs(t, err, ErrAttached)
}

func Test_unit_Attach_Attached_Fail(t *testing.T) {
	p := New()
	buf := p.Get()
	err := p.Attach(buf)
	assert.ErrorIs(t, err, ErrAttached)
}

func Test_unit_Pooled_Get(t *testing.T) {
	p := New()
	buf := p.GetFrom([]byte("test"))
	buf.Recycle()
	buf = p.Get()
	assert.Equal(t, 0, len(buf.buf))
	assert.Same(t, p, buf.pool)
	assert.Equal(t, 0, buf.strikes)
}

func Test_unit_Strikes(t *testing.T) {
	tests := []struct {
		name              string
		size, cap         int
		initial, expected int
		kept              bool
	}{
		{"Small", 1, 2, 5, 0, true},
		{"Utilized", 70000, 100000, 5, 0, true},
		{"Large underutilized", 1, 100000, 2, 3, true},
		{"Large underutilized capped", 1, 100000, 4, 4, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &poolStorage{strikes: tt.initial, buf: make([]byte, tt.size, tt.cap)}
			kept := st.recycle()
			assert.Equal(t, tt.kept, kept)
			assert.Equal(t, tt.expected, st.strikes)
		})
	}
}

func Test_unit_Strikes_Read(t *testing.T) {
	p := New()
	buf := p.Get()
	_, _ = buf.Write(make([]byte, 100000))
	_, _ = io.Copy(io.Discard, buf)
	buf.strikes = 999
	// The heuristic measures utilization by written length, not unread length,
	// so a fully-read large buffer still counts as well-utilized.
	kept := buf.recycle()
	assert.True(t, kept)
	assert.Equal(t, 0, buf.strikes)
}

func Test_unit_Concurrent(t *testing.T) {
	p := New()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				buf := p.Get()
				_, _ = buf.WriteString("hello")
				data, err := ReadAllBytes(buf)
				if err != nil || string(data) != "hello" {
					t.Errorf("got %q, %v", data, err)
					return
				}
				buf.Recycle()
			}
		}()
	}
	wg.Wait()
}
