// Memory-retention probe: how long does a pool keep handing out an oversized
// backing array after a rare large request, and how much allocation churn does
// its eviction policy cost?
//
// Workload: 100k get→write→release cycles of 1 KiB, with a 1 MiB spike every
// 1000th cycle. After each spike we count how many subsequent small cycles
// still receive a buffer with cap > 128 KiB ("pinned hands"). Also reports
// total allocation churn and observed GC cycles.
//
// Run one implementation per process: retention -impl <name>
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"runtime"
	"sync"

	"github.com/JohanLindvall/bufpool"
	"github.com/oxtoacart/bpool"
	"github.com/valyala/bytebufferpool"
)

const (
	iters     = 100_000
	spikeEach = 1000
	smallN    = 1024
	spikeN    = 1 << 20
	bigCap    = 128 << 10
)

// cycle writes payload into a pooled buffer and returns the capacity of the
// backing array that the pool handed out for this cycle.
type cycleFunc func(payload []byte) (handedCap int)

func main() {
	implName := flag.String("impl", "", "implementation to probe")
	flag.Parse()

	small := make([]byte, smallN)
	spike := make([]byte, spikeN)

	var cycle cycleFunc
	switch *implName {
	case "bufpool":
		p := new(bufpool.Pool)
		cycle = func(payload []byte) int {
			b := p.Get()
			c := cap(b.Bytes())
			_, _ = b.Write(payload)
			b.Release()
			return c
		}
	case "bytesbuffer_syncpool":
		p := sync.Pool{New: func() any { return new(bytes.Buffer) }}
		cycle = func(payload []byte) int {
			b := p.Get().(*bytes.Buffer)
			b.Reset()
			c := b.Cap()
			_, _ = b.Write(payload)
			p.Put(b)
			return c
		}
	case "byteslice_syncpool":
		p := sync.Pool{New: func() any { return new([]byte) }}
		cycle = func(payload []byte) int {
			bp := p.Get().(*[]byte)
			b := (*bp)[:0]
			c := cap(b)
			b = append(b, payload...)
			*bp = b
			p.Put(bp)
			return c
		}
	case "bytebufferpool":
		p := new(bytebufferpool.Pool)
		cycle = func(payload []byte) int {
			b := p.Get()
			c := cap(b.B)
			_, _ = b.Write(payload)
			p.Put(b)
			return c
		}
	case "bpool":
		p := bpool.NewBufferPool(64)
		cycle = func(payload []byte) int {
			b := p.Get()
			c := b.Cap()
			_, _ = b.Write(payload)
			p.Put(b)
			return c
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown -impl %q\n", *implName)
		os.Exit(2)
	}

	var before runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	pinnedHands := 0 // small cycles served by a >128KiB backing array
	smallHands := 0
	spikes := 0
	maxHeap := uint64(0)
	for i := 1; i <= iters; i++ {
		payload := small
		if i%spikeEach == 0 {
			payload = spike
			spikes++
			cycle(payload)
			continue
		}
		smallHands++
		if c := cycle(payload); c > bigCap {
			pinnedHands++
		}
		if i%1000 == 500 {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > maxHeap {
				maxHeap = m.HeapAlloc
			}
		}
	}

	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	fmt.Printf("%-22s smallCycles=%d spikes=%d pinnedHands=%d (%.1f%%) churnMB=%.1f maxHeapMB=%.1f numGC=%d\n",
		*implName, smallHands, spikes,
		pinnedHands, 100*float64(pinnedHands)/float64(smallHands),
		float64(after.TotalAlloc-before.TotalAlloc)/(1<<20),
		float64(maxHeap)/(1<<20),
		after.NumGC-before.NumGC)
}
