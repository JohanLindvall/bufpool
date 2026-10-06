// SPDX-License-Identifier: MIT

package bufpool_test

import (
	"fmt"
	"io"
	"os"

	"github.com/JohanLindvall/bufpool"
)

// A Buffer drawn from a Pool is an io.Writer and an io.Reader, like a
// bytes.Buffer; Release returns it, backing array and all, to the pool.
func Example() {
	var pool bufpool.Pool // the zero value is ready to use

	buf := pool.Get()
	defer buf.Release() // back to the pool; buf and slices from it are invalid after this

	_, _ = fmt.Fprintf(buf, "hello, %s", "gopher") // a Buffer is an io.Writer…
	_, _ = io.Copy(os.Stdout, buf)                 // …and an io.Reader
	// Output: hello, gopher
}

// Close returns a buffer to its pool, so a function can hand one off as an
// io.ReadCloser and leave the release to whoever consumes it: no pool
// reference is needed at the release site. See Buffer.Close before handing
// one to net/http.
func Example_readCloser() {
	var pool bufpool.Pool

	render := func(name string) io.ReadCloser {
		buf := pool.Get()
		_, _ = fmt.Fprintf(buf, "hello, %s", name)
		return buf // the consumer's Close returns it to the pool
	}

	body := render("gopher")
	_, _ = io.Copy(os.Stdout, body)
	_ = body.Close() // returns the buffer to the pool
	// Output: hello, gopher
}
