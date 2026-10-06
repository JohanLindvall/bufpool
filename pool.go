// SPDX-License-Identifier: MIT

package bufpool

import (
	"sync"
)

// Pool is a pool of reusable byte buffers, backed by a sync.Pool. The zero
// value is ready to use. A Pool is safe for concurrent use by multiple
// goroutines and must not be copied after first use.
type Pool struct {
	pool sync.Pool
}

// Get returns an empty Buffer drawn from the pool. The buffer is attached to
// p, so calling Release or Close on it puts it back into the pool.
func (p *Pool) Get() *Buffer {
	storage, ok := p.pool.Get().(*poolStorage)
	if !ok {
		storage = new(poolStorage)
	}
	result := &Buffer{poolStorage: *storage, storage: storage, pool: p}
	result.buf = result.buf[:0]
	return result
}

// keep applies the strike heuristic that decides whether b's backing array is
// worth keeping; the package documentation describes the policy and its
// consequences, and is the copy to keep current. Each call counts as one
// application, so Reset followed by Release charges two strikes. keep must be
// called before the length is truncated, as it reads the current utilization,
// which is measured on the written length rather than the unread length: the
// most common pooled pattern is fill, drain, release, and scoring that at 0%
// would strike out every large array every five cycles.
func (b *poolStorage) keep() bool {
	switch {
	case cap(b.buf) <= 1<<16: // always keep buffers of at most 64KiB
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
	if b.keep() {
		p.pool.Put(b)
	}
}

type poolStorage struct {
	strikes int
	buf     []byte
}

// noCopy makes go vet's copylocks check flag by-value copies of a containing
// struct. It has no runtime effect.
type noCopy struct{}

func (*noCopy) Lock()   {}
func (*noCopy) Unlock() {}
