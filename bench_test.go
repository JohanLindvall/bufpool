package bufpool

import (
	"testing"
)

func BenchmarkGetRecycle(b *testing.B) {
	p := New()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		buf := p.Get()
		_, _ = buf.WriteString("hello world")
		buf.Recycle()
	}
}

func BenchmarkGetRecycleParallel(b *testing.B) {
	p := New()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			buf := p.Get()
			_, _ = buf.WriteString("hello world")
			buf.Recycle()
		}
	})
}

func BenchmarkWrite(b *testing.B) {
	p := New()
	buf := p.Get()
	defer buf.Recycle()
	data := make([]byte, 4096)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.Reset()
		_, _ = buf.Write(data)
	}
}
