package bufpool

import (
	"io"
	"reflect"
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
		// Boundary rows: without these, the size threshold is only pinned to
		// the open range [3, 100000) and the utilization threshold not at all,
		// so lowering 1<<16 an octave, flipping either <= to <, or widening
		// cap/2 to cap/4 all pass. cap == 1<<16 is reached by real code: Grow's
		// ladder passes exact powers of two.
		{"64KiB exactly", 0, 1 << 16, 3, 0, true},
		{"Just over 64KiB", 0, 1<<16 + 1, 0, 1, true},
		{"Exactly 50% utilized", 50000, 100000, 3, 0, true},
		{"Just under 50% utilized", 49999, 100000, 0, 1, true},
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

// The four tests below pin the pool's reason to exist: that a backing array
// survives a Release/Get round trip, and that an oversized under-utilized one
// does not. Statement coverage cannot stand in for them — Pool.put's discard
// case is an implicit empty else with no statements — so without these,
// mutating Get to drop the array, Release to hand over an empty storage, put
// to never evict, or put to never Put all pass at 100%.

func Test_unit_Release_HandsArrayToStorage(t *testing.T) {
	p := new(Pool)
	buf := p.Get()
	buf.Grow(4096)
	storage := buf.storage
	buf.Release()
	assert.Equal(t, 4096, cap(storage.buf), "Release must copy the grown array into the pooled storage")
}

// poolRounds bounds the retry loops below. A single Put/Get round trip is not
// a reliable signal either way: under -race, sync.Pool.Put deliberately drops
// one item in four (see the race.Enabled branch in sync/pool.go), and a GC
// between Put and Get can drain the pool entirely. Because a miss only ever
// means the item never reached the pool — never that Get failed to reuse one —
// repeating preserves exactly what these tests detect while removing the
// flake. Single-shot assertions here failed roughly a third of CI's
// `go test -race -shuffle=on` runs.
const poolRounds = 100

func Test_unit_Get_ReusesPooledArray(t *testing.T) {
	p := new(Pool)
	for i := 0; i < poolRounds; i++ {
		p.pool.Put(&poolStorage{buf: make([]byte, 0, 4096)})
		if p.Get().Cap() == 4096 {
			return
		}
	}
	t.Fatal("Get must reuse the pooled array, not drop it")
}

func Test_unit_Put_EvictsOversizedUnderUtilized(t *testing.T) {
	// Inverted relative to the others: this asserts the array never comes back,
	// so it repeats rather than retries. A single round would also pass against
	// a pool that wrongly kept the array, on any run where -race dropped the
	// Put. (Looping put over one storage would not work either — the first four
	// calls Put the same storage back.)
	p := new(Pool)
	for i := 0; i < poolRounds; i++ {
		p.put(&poolStorage{strikes: 4, buf: make([]byte, 0, 1<<17)})
		if c := p.Get().Cap(); c != 0 {
			t.Fatalf("an oversized under-utilized array must not re-enter the pool (got cap %d)", c)
		}
	}
}

func Test_unit_ReleaseGet_ReusesBackingArray(t *testing.T) {
	// End-to-end: the three tests above all still pass if Pool.put never puts.
	p := new(Pool)
	for i := 0; i < poolRounds; i++ {
		buf := p.Get()
		buf.Grow(4096)
		buf.Release()
		if p.Get().Cap() == 4096 {
			return
		}
	}
	t.Fatal("a released array must come back out of the pool")
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
	nc.Lock()
	defer nc.Unlock()
	assert.NotNil(t, &nc)
}

func Test_unit_Buffer_CarriesCopyGuard(t *testing.T) {
	// The copy-by-value protection the package advertises is a property of
	// Buffer's layout, not of any statement, so nothing else in the suite
	// notices if the noCopy field is dropped in a refactor: vet stays clean
	// (it has no Buffer copy in this module to fire on) and coverage stays at
	// 100%. Assert the property directly instead. Take the type through a nil
	// pointer so this check does not itself copy a Buffer.
	locker := reflect.TypeOf((*sync.Locker)(nil)).Elem()
	bt := reflect.TypeOf((*Buffer)(nil)).Elem()
	for i := 0; i < bt.NumField(); i++ {
		if reflect.PointerTo(bt.Field(i).Type).Implements(locker) {
			return
		}
	}
	t.Fatalf("no field of Buffer implements sync.Locker, so go vet's copylocks "+
		"check no longer flags by-value copies; fields: %d", bt.NumField())
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
