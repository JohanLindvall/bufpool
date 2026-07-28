package bufpool

import (
	"io"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_unit_ZeroValuePool(t *testing.T) {
	// The zero Pool must be usable without a constructor.
	var p Pool
	buf := p.Get()
	assert.NotNil(t, buf)
	assert.Equal(t, 0, buf.Len())
	_, _ = buf.WriteString("test")
	buf.Release()
	buf = p.Get()
	assert.Equal(t, 0, buf.Len())
}

func Test_unit_Get(t *testing.T) {
	p := new(Pool)
	buf := p.Get()
	assert.NotNil(t, buf)
	assert.Equal(t, 0, buf.Len())
}

func Test_unit_Pooled_Get(t *testing.T) {
	p := new(Pool)
	buf := p.Get()
	_, _ = buf.Write([]byte("test"))
	buf.Release()
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
			kept := st.keep()
			assert.Equal(t, tt.kept, kept)
			assert.Equal(t, tt.expected, st.strikes)
		})
	}
}

func Test_unit_Strikes_Read(t *testing.T) {
	p := new(Pool)
	buf := p.Get()
	_, _ = buf.Write(make([]byte, 100000))
	_, _ = io.Copy(io.Discard, buf)
	buf.strikes = 999
	// The heuristic measures utilization by written length, not unread length,
	// so a fully-read large buffer still counts as well-utilized.
	kept := buf.keep()
	assert.True(t, kept)
	assert.Equal(t, 0, buf.strikes)
}

func Test_unit_NoCopy(t *testing.T) {
	// noCopy's methods exist only so go vet's copylocks check fires on
	// by-value copies of Buffer; exercise them so they are not dead code.
	var nc noCopy
	assert.NotPanics(t, func() { nc.Lock(); nc.Unlock() })
}

func Test_unit_Concurrent(t *testing.T) {
	var p Pool
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
				buf.Release()
			}
		}()
	}
	wg.Wait()
}
