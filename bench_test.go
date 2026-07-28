package bufpool

import (
	"testing"
)

func BenchmarkGetRelease(b *testing.B) {
	var p Pool
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		buf := p.Get()
		_, _ = buf.WriteString("hello world")
		buf.Release()
	}
}

func BenchmarkGetReleaseParallel(b *testing.B) {
	var p Pool
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			buf := p.Get()
			_, _ = buf.WriteString("hello world")
			buf.Release()
		}
	})
}

func BenchmarkWrite(b *testing.B) {
	var p Pool
	buf := p.Get()
	defer buf.Release()
	data := make([]byte, 4096)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.Reset()
		_, _ = buf.Write(data)
	}
}
