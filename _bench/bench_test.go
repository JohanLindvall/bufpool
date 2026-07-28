// Speed comparison: bufpool vs common alternatives.
//
// Implementations compared:
//   - bufpool:        github.com/JohanLindvall/bufpool
//   - bytesbuffer:    sync.Pool of *bytes.Buffer (the stdlib idiom)
//   - byteslice:      sync.Pool of *[]byte (the rawest idiom)
//   - bytebufferpool: github.com/valyala/bytebufferpool
//   - bpool:          github.com/oxtoacart/bpool (channel-based, bounded)
package compare

import (
	"bytes"
	"sync"
	"testing"

	"github.com/JohanLindvall/bufpool"
	"github.com/oxtoacart/bpool"
	"github.com/valyala/bytebufferpool"
)

var small = []byte("hello world")
var large = make([]byte, 4096)

type impl struct {
	name  string
	cycle func(payload []byte)
}

func impls() []impl {
	bp := new(bufpool.Pool)
	var bbSync = sync.Pool{New: func() any { return new(bytes.Buffer) }}
	var bsSync = sync.Pool{New: func() any { return new([]byte) }}
	bbp := new(bytebufferpool.Pool)
	obp := bpool.NewBufferPool(64)

	return []impl{
		{"bufpool", func(p []byte) {
			b := bp.Get()
			_, _ = b.Write(p)
			sinkLen = b.Len()
			b.Release()
		}},
		{"bytesbuffer_syncpool", func(p []byte) {
			b := bbSync.Get().(*bytes.Buffer)
			b.Reset()
			_, _ = b.Write(p)
			sinkLen = b.Len()
			bbSync.Put(b)
		}},
		{"byteslice_syncpool", func(p []byte) {
			bp := bsSync.Get().(*[]byte)
			b := (*bp)[:0]
			b = append(b, p...)
			sinkLen = len(b)
			*bp = b
			bsSync.Put(bp)
		}},
		{"bytebufferpool", func(p []byte) {
			b := bbp.Get()
			_, _ = b.Write(p)
			sinkLen = b.Len()
			bbp.Put(b)
		}},
		{"bpool", func(p []byte) {
			b := obp.Get()
			_, _ = b.Write(p)
			sinkLen = b.Len()
			obp.Put(b)
		}},
	}
}

var sinkLen int

func benchCycle(b *testing.B, payload []byte) {
	for _, im := range impls() {
		b.Run(im.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(payload)))
			for i := 0; i < b.N; i++ {
				im.cycle(payload)
			}
		})
	}
}

func benchCycleParallel(b *testing.B, payload []byte) {
	for _, im := range impls() {
		b.Run(im.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(payload)))
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					im.cycle(payload)
				}
			})
		})
	}
}

func BenchmarkSmall(b *testing.B)         { benchCycle(b, small) }
func BenchmarkLarge4K(b *testing.B)       { benchCycle(b, large) }
func BenchmarkSmallParallel(b *testing.B) { benchCycleParallel(b, small) }
func BenchmarkLarge4KParallel(b *testing.B) {
	benchCycleParallel(b, large)
}
