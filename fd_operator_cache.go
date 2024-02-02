package znet

import (
	"runtime"
	"sync/atomic"
	"unsafe"
)

const opSize = unsafe.Sizeof(FDOperator{})

func allocOp() *FDOperator {
	return opcache.alloc()
}

func freeOp(op *FDOperator) {
	opcache.free(op)
}

func init() {
	opcache = &operatorCache{
		// cache: make(map[int][]byte),
		cache: make([]*FDOperator, 0, 1024),
	}
	runtime.KeepAlive(opcache)
}

var opcache *operatorCache

type operatorCache struct {
	locked int32
	first  *FDOperator
	cache  []*FDOperator
}

//从cache中获取一个FDOperator

func (c *operatorCache) alloc() *FDOperator {
	c.lock()
	if c.first == nil {
		n := block4k / opSize
		if n == 0 {
			n = 1
		}
		// Must be in non-GC memory because can be referenced
		// only from epoll/kqueue internals.
		for i := uintptr(0); i < n; i++ {
			pd := &FDOperator{}
			c.cache = append(c.cache, pd)
			pd.next = c.first
			c.first = pd //指向第一个pd
		}
	}
	op := c.first
	c.first = op.next
	c.unlock()
	return op
}

//归还FDOperator

func (c *operatorCache) free(op *FDOperator) {
	op.unused()
	op.reset()

	c.lock()
	op.next = c.first
	c.first = op
	c.unlock()
}

func (c *operatorCache) lock() {
	for !atomic.CompareAndSwapInt32(&c.locked, 0, 1) {
		runtime.Gosched()
	}
}

func (c *operatorCache) unlock() {
	atomic.StoreInt32(&c.locked, 0)
}
