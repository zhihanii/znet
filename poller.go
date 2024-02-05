package znet

import (
	"github.com/zhihanii/zlog"
	"runtime"
	"sync/atomic"
	"syscall"
	"unsafe"
)

type Poller interface {
	Poll() error

	Close() error

	Control(operator *FDOperator, event EpollEvent) error

	//StoreConnection(fd int, c *connection)
}

type EpollEvent int8

const (
	EpollRead    EpollEvent = 0x1
	EpollWrite   EpollEvent = 0x2
	EpollDetach  EpollEvent = 0x3
	EpollModRead EpollEvent = 0x4
	EpollR2RW    EpollEvent = 0x5
	EpollRW2R    EpollEvent = 0x6
)

func openPoller() Poller {
	return openDefaultPoller()
}

type defaultPoller struct {
	size     int
	capacity int
	events   []epollevent
	barriers []barrier
	hups     []func(Poller) error
	fd       int
	wop      *FDOperator
	buf      []byte
	trigger  uint32
}

func openDefaultPoller() *defaultPoller {
	var p = new(defaultPoller)
	var err error
	p.buf = make([]byte, 8)
	p.fd, err = syscall.EpollCreate1(0)
	if err != nil {
		panic(err)
	}
	var r0, _, e0 = syscall.Syscall(syscall.SYS_EVENTFD2, 0, 0, 0)
	if e0 != 0 {
		syscall.Close(p.fd)
		panic(err)
	}
	p.wop = &FDOperator{FD: int(r0)}
	p.Control(p.wop, EpollRead)
	return p
}

func (p *defaultPoller) reset(size, capacity int) {
	p.size, p.capacity = size, capacity
	p.events, p.barriers = make([]epollevent, size), make([]barrier, size)
	for i := range p.barriers {
		p.barriers[i].bs = make([][]byte, p.capacity)
		p.barriers[i].ivs = make([]syscall.Iovec, p.capacity)
	}
}

func (p *defaultPoller) Poll() (err error) {
	// init
	var caps, msec, n = barriercap, -1, 0
	p.reset(128, caps)
	// wait
	for {
		if n == p.size && p.size < 128*1024 {
			p.reset(p.size<<1, caps)
		}
		n, err = EpollWait(p.fd, p.events, msec)
		if err != nil && err != syscall.EINTR {
			return err
		}
		if n <= 0 {
			msec = -1
			runtime.Gosched()
			continue
		}
		msec = 0
		if p.handle(p.events[:n]) {
			return nil
		}
	}
}

func (p *defaultPoller) handle(events []epollevent) (closed bool) {
	for i := range events {
		var operator = *(**FDOperator)(unsafe.Pointer(&events[i].data))
		if !operator.tryOnEvent() {
			continue
		}

		if operator.FD == p.wop.FD {
			syscall.Read(p.wop.FD, p.buf)
			atomic.StoreUint32(&p.trigger, 0)
			if p.buf[0] > 0 {
				syscall.Close(p.wop.FD)
				syscall.Close(p.fd)
				operator.done()
				return true
			}
			operator.done()
			continue
		}

		evt := events[i].events
		//todo 区分连接和非连接
		if evt&syscall.EPOLLIN != 0 {
			if operator.isConnection {
				var bs = operator.Inputs(p.barriers[i].bs)
				if len(bs) > 0 {
					var n, err = readv(operator.FD, bs, p.barriers[i].ivs)
					operator.InputAck(n)
					if err != nil && err != syscall.EAGAIN && err != syscall.EINTR {
						zlog.Errorf("readv(fd=%d) failed: %s", operator.FD, err.Error())
						p.appendHup(operator)
						continue
					}
				}
			} else {
				operator.OnAccept()
			}
		}

		if evt&(syscall.EPOLLHUP|syscall.EPOLLRDHUP) != 0 {
			p.appendHup(operator)
			continue
		}

		if evt&syscall.EPOLLERR != 0 {
			// Under block-zerocopy, the kernel may give an error callback, which is not a real error, just an EAGAIN.
			// So here we need to check this error, if it is EAGAIN then do nothing, otherwise still mark as hup.
			if _, _, _, _, err := syscall.Recvmsg(operator.FD, nil, nil, syscall.MSG_ERRQUEUE); err != syscall.EAGAIN {
				p.appendHup(operator)
			} else {
				operator.done()
			}
			continue
		}

		if evt&syscall.EPOLLOUT != 0 {
			zlog.Infof("epoll_out event")
			if operator.isConnection {
				var bs, supportZeroCopy = operator.Outputs(p.barriers[i].bs)
				if len(bs) > 0 {
					// TODO: Let the upper layer pass in whether to use ZeroCopy.
					var n, err = sendmsg(operator.FD, bs, p.barriers[i].ivs, false && supportZeroCopy)
					operator.OutputAck(n)
					if err != nil && err != syscall.EAGAIN {
						zlog.Errorf("sendmsg(fd=%d) failed: %s", operator.FD, err.Error())
						p.appendHup(operator)
						continue
					}
				}
			}
		}
		operator.done()
	}
	p.detaches()
	return false
}

func (p *defaultPoller) Close() error {
	_, err := syscall.Write(p.wop.FD, []byte{1, 0, 0, 0, 0, 0, 0, 0})
	return err
}

func (p *defaultPoller) Trigger() error {
	if atomic.AddUint32(&p.trigger, 1) > 1 {
		return nil
	}
	// MAX(eventfd) = 0xfffffffffffffffe
	_, err := syscall.Write(p.wop.FD, []byte{0, 0, 0, 0, 0, 0, 0, 1})
	return err
}

func (p *defaultPoller) Control(operator *FDOperator, event EpollEvent) error {
	var op int
	var evt epollevent
	*(**FDOperator)(unsafe.Pointer(&evt.data)) = operator
	switch event {
	case EpollRead:
		operator.inuse()
		op, evt.events = syscall.EPOLL_CTL_ADD, syscall.EPOLLIN|syscall.EPOLLRDHUP|syscall.EPOLLERR
	case EpollModRead:
		operator.inuse()
		op, evt.events = syscall.EPOLL_CTL_MOD, syscall.EPOLLIN|syscall.EPOLLRDHUP|syscall.EPOLLERR
	case EpollDetach:
		op, evt.events = syscall.EPOLL_CTL_DEL, syscall.EPOLLIN|syscall.EPOLLOUT|syscall.EPOLLRDHUP|syscall.EPOLLERR
	case EpollWrite:
		operator.inuse()
		op, evt.events = syscall.EPOLL_CTL_ADD, EPOLLET|syscall.EPOLLOUT|syscall.EPOLLRDHUP|syscall.EPOLLERR
	case EpollR2RW:
		op, evt.events = syscall.EPOLL_CTL_MOD, syscall.EPOLLIN|syscall.EPOLLOUT|syscall.EPOLLRDHUP|syscall.EPOLLERR
	case EpollRW2R:
		op, evt.events = syscall.EPOLL_CTL_MOD, syscall.EPOLLIN|syscall.EPOLLRDHUP|syscall.EPOLLERR
	}
	return EpollCtl(p.fd, op, operator.FD, &evt)
}

func (p *defaultPoller) appendHup(operator *FDOperator) {
	p.hups = append(p.hups, operator.OnHup)
	//删除注册的事件
	operator.Control(EpollDetach)
	operator.done()
}

func (p *defaultPoller) detaches() {
	if len(p.hups) == 0 {
		return
	}
	hups := p.hups
	p.hups = nil
	go func(hups []func(p Poller) error) {
		for i := range hups {
			if hups[i] != nil {
				hups[i](p)
			}
		}
	}(hups)
}
