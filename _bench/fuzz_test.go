// SPDX-License-Identifier: MIT

// Differential fuzz: bufpool.Buffer against bytes.Buffer for the operations
// whose semantics the package documents as matching (Write, WriteString,
// WriteByte, Read, ReadByte, Next, Len, Bytes, WriteTo, Reset).
package compare

import (
	"bytes"
	"testing"

	"github.com/JohanLindvall/bufpool"
)

// chunk is the source of written bytes. It is large enough that a single write
// can be arg*arg bytes for arg up to 255, so a program can push capacity past
// the 64 KiB threshold where the keep-or-discard heuristic starts counting
// strikes. With writes capped at 31 bytes, as they once were, Reset's discard
// branch was unreachable: 69M executions over the accumulated corpus never
// produced a buffer above 16 KiB.
var chunk = bytes.Repeat([]byte("0123456789abcdef"), 255*255/16+1)

func FuzzDifferential(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4, 5})
	f.Add([]byte{2, 200, 0, 50, 2, 10, 4, 3})
	// Two large writes (cap doubles past 64 KiB) then six Resets, the shortest
	// path to the discard branch: the first Reset still sees a full buffer and
	// clears the strike counter, so five under-utilized ones must follow before
	// the sixth discards.
	f.Add([]byte{0, 255, 0, 255, 5, 0, 5, 0, 5, 0, 5, 0, 5, 0, 5, 0})
	// Recycle through the pool between writes, exercising Get's reuse branch.
	f.Add([]byte{0, 40, 9, 0, 0, 40, 9, 0, 3, 0})
	f.Fuzz(func(t *testing.T, program []byte) {
		var pool bufpool.Pool
		got := pool.Get()
		// Deferred through a closure: op 9 rebinds got.
		defer func() { got.Release() }()
		want := new(bytes.Buffer)

		for i := 0; i+1 < len(program); i += 2 {
			op, arg := program[i]%10, int(program[i+1])
			switch op {
			case 0: // Write
				g, _ := got.Write(chunk[:arg*arg])
				w, _ := want.Write(chunk[:arg*arg])
				if g != w {
					t.Fatalf("op %d Write: n=%d want %d", i, g, w)
				}
			case 1: // WriteString
				s := string(chunk[:arg*arg])
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
			case 6: // WriteByte
				gerr := got.WriteByte(byte(arg))
				werr := want.WriteByte(byte(arg))
				if (gerr == nil) != (werr == nil) {
					t.Fatalf("op %d WriteByte: %v want %v", i, gerr, werr)
				}
			case 7: // ReadByte
				gc, gerr := got.ReadByte()
				wc, werr := want.ReadByte()
				if gc != wc || (gerr == nil) != (werr == nil) {
					t.Fatalf("op %d ReadByte: (%q,%v) want (%q,%v)", i, gc, gerr, wc, werr)
				}
			case 8: // Next
				g, w := got.Next(arg), want.Next(arg)
				if !bytes.Equal(g, w) {
					t.Fatalf("op %d Next(%d): %q want %q", i, arg, g, w)
				}
			case 9: // recycle through the pool, exercising Pool.Get's reuse branch
				// The pool is per-iteration on purpose: hoisting it to package
				// scope would make iterations order-dependent and violate Go
				// fuzzing's determinism requirement, so a minimized reproducer
				// would not reproduce when re-run standalone. A Release
				// immediately followed by a Get still hits sync.Pool's per-P
				// private slot, so the array really is recycled.
				got.Release()
				got = pool.Get()
				want.Reset()
			}
		}
		if !bytes.Equal(got.Bytes(), want.Bytes()) || got.Len() != want.Len() {
			t.Fatalf("final state: %q (len %d) want %q (len %d)", got.Bytes(), got.Len(), want.Bytes(), want.Len())
		}
	})
}
