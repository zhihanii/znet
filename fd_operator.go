package znet

import (
	"runtime"
	"sync/atomic"
)

type FDOperator struct {
	FD int

	OnAccept func() error
	OnHup    func(Poller) error

	// The following is the required fn, which must exist when used, or directly panic.
	// Fns are only called by the poll when handles connection events.
	Inputs   func(vs [][]byte) (rs [][]byte)
	InputAck func(n int) (err error)

	// Outputs will locked if len(rs) > 0, which need unlocked by OutputAck.
	Outputs   func(vs [][]byte) (rs [][]byte, supportZeroCopy bool)
	OutputAck func(n int) (err error)

	poller Poller

	next         *FDOperator
	state        int32 // CAS: 0(unused) 1(inuse) 2(onEvent:正在响应事件)
	isConnection bool
}

func (o *FDOperator) Control(event EpollEvent) error {
	return o.poller.Control(o, event)
}

func (o *FDOperator) isUnused() bool {
	return atomic.LoadInt32(&o.state) == 0
}

func (o *FDOperator) unused() {
	for !atomic.CompareAndSwapInt32(&o.state, 1, 0) {
		if atomic.LoadInt32(&o.state) == 0 {
			return
		}
		runtime.Gosched()
	}
}

func (o *FDOperator) inuse() {
	for !atomic.CompareAndSwapInt32(&o.state, 0, 1) {
		if atomic.LoadInt32(&o.state) == 1 {
			return
		}
		runtime.Gosched()
	}
}

// 尝试转换为onEvent状态
func (o *FDOperator) tryOnEvent() (ok bool) {
	return atomic.CompareAndSwapInt32(&o.state, 1, 2)
}

// 完成事件响应, 转换为inuse状态
func (o *FDOperator) done() {
	atomic.StoreInt32(&o.state, 1)
}

func (o *FDOperator) reset() {
	o.FD = 0
	o.poller = nil
}
