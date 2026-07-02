package bufpool

import (
	"errors"
	"sync"
)

// ErrAttached is returned by Pool.Attach when the buffer is already attached
// to a pool.
var ErrAttached = errors.New("buffer is already attached")

// Pool is a pool of reusable byte buffers, backed by a sync.Pool. The zero
// value is not usable; create one with New. A Pool is safe for concurrent use
// by multiple goroutines.
type Pool struct {
	pool sync.Pool
}

// New creates a new, empty buffer pool.
func New() *Pool {
	return &Pool{pool: sync.Pool{New: func() any { return new(poolStorage) }}}
}

// Get returns an empty Buffer drawn from the pool. The buffer is attached to
// p, so calling Recycle or Close on it puts it back into the pool.
func (p *Pool) Get() *Buffer {
	storage := p.pool.Get().(*poolStorage)
	result := &Buffer{poolStorage: *storage, storage: storage, pool: p}
	result.buf = result.buf[:0]
	return result
}

// GetFrom returns a Buffer pre-filled with a copy of data. The data is copied,
// so the caller may reuse or modify data afterwards. Like Get, the returned
// buffer is attached to p.
func (p *Pool) GetFrom(data []byte) *Buffer {
	buf := p.Get()
	_, _ = buf.Write(data)
	return buf
}

// Attach attaches a detached buffer to the pool so that a later Recycle or Close
// recycles it into p. It returns ErrAttached if b is already attached to a pool.
func (p *Pool) Attach(b *Buffer) error {
	if b.pool != nil {
		return ErrAttached
	}
	b.pool = p
	return nil
}

// recycle applies the strike heuristic that decides whether b's backing array is
// worth keeping. Small buffers and sufficiently-utilized ones are always kept (and
// the strike counter cleared); an oversized, under-utilized buffer is given up to
// four consecutive strikes before recycle reports false (discard), so a single
// large usage is not kept alive by a continuous stream of small ones. It must be
// called before the length is truncated, as it reads the current utilization.
func (b *poolStorage) recycle() bool {
	switch {
	case cap(b.buf) <= 1<<16: // always recycle buffers smaller than 64KiB
		b.strikes = 0
	case cap(b.buf)/2 <= len(b.buf): // at least 50% utilization
		b.strikes = 0
	case b.strikes < 4:
		b.strikes++
	default:
		return false // discard the buffer; too large and too often under-utilized
	}
	return true
}

func (p *Pool) put(b *poolStorage) {
	if b.recycle() {
		p.pool.Put(b)
	}
}

type poolStorage struct {
	strikes int
	buf     []byte
}
