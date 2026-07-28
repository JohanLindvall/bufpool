// Differential fuzz: bufpool.Buffer against bytes.Buffer for the operations
// whose semantics the package documents as matching (Write, WriteString, Read,
// Len, Bytes, WriteTo, Reset).
package compare

import (
	"bytes"
	"testing"

	"github.com/JohanLindvall/bufpool"
)

func FuzzDifferential(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4, 5})
	f.Add([]byte{2, 200, 0, 50, 2, 10, 4, 3})
	f.Fuzz(func(t *testing.T, program []byte) {
		var pool bufpool.Pool
		got := pool.Get()
		defer got.Release()
		want := new(bytes.Buffer)

		chunk := []byte("0123456789abcdef0123456789abcdef")
		for i := 0; i+1 < len(program); i += 2 {
			op, arg := program[i]%6, int(program[i+1])
			switch op {
			case 0: // Write
				g, _ := got.Write(chunk[:arg%len(chunk)])
				w, _ := want.Write(chunk[:arg%len(chunk)])
				if g != w {
					t.Fatalf("op %d Write: n=%d want %d", i, g, w)
				}
			case 1: // WriteString
				s := string(chunk[:arg%len(chunk)])
				g, _ := got.WriteString(s)
				w, _ := want.WriteString(s)
				if g != w {
					t.Fatalf("op %d WriteString: n=%d want %d", i, g, w)
				}
			case 2: // Read (1..256 bytes; len(p)==0 divergence is allowed by io.Reader)
				n := arg + 1
				pg, pw := make([]byte, n), make([]byte, n)
				gn, gerr := got.Read(pg)
				wn, werr := want.Read(pw)
				if gn != wn || (gerr == nil) != (werr == nil) || !bytes.Equal(pg[:gn], pw[:wn]) {
					t.Fatalf("op %d Read(%d): (%d,%v,%q) want (%d,%v,%q)", i, n, gn, gerr, pg[:gn], wn, werr, pw[:wn])
				}
			case 3: // Bytes + Len
				if !bytes.Equal(got.Bytes(), want.Bytes()) {
					t.Fatalf("op %d Bytes: %q want %q", i, got.Bytes(), want.Bytes())
				}
				if got.Len() != want.Len() {
					t.Fatalf("op %d Len: %d want %d", i, got.Len(), want.Len())
				}
			case 4: // WriteTo drains everything
				var dg, dw bytes.Buffer
				gn, gerr := got.WriteTo(&dg)
				wn, werr := want.WriteTo(&dw)
				if gn != wn || (gerr == nil) != (werr == nil) || !bytes.Equal(dg.Bytes(), dw.Bytes()) {
					t.Fatalf("op %d WriteTo: (%d,%v,%q) want (%d,%v,%q)", i, gn, gerr, dg.Bytes(), wn, werr, dw.Bytes())
				}
			case 5: // Reset
				got.Reset()
				want.Reset()
			}
		}
		if !bytes.Equal(got.Bytes(), want.Bytes()) || got.Len() != want.Len() {
			t.Fatalf("final state: %q (len %d) want %q (len %d)", got.Bytes(), got.Len(), want.Bytes(), want.Len())
		}
	})
}
