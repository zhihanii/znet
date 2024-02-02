package znet

import (
	"context"
	"net"
	"runtime"
)

type EventLoop interface {
	Serve(listener net.Listener) error
	Shutdown(ctx context.Context) error
}

func NewEventLoop(eh EventHandler, opts ...Option) EventLoop {
	o := new(options)
	for _, opt := range opts {
		opt(o)
	}
	return &eventLoop{
		o:    o,
		stop: make(chan error, 1),
		eh:   eh,
	}
}

type eventLoop struct {
	o    *options
	s    *server
	stop chan error
	eh   EventHandler
}

func (e *eventLoop) Serve(netListener net.Listener) error {
	l, err := ConvertListener(netListener)
	if err != nil {
		return err
	}
	e.s = newServer(l, e.eh, e.o)
	e.s.Run()

	err = e.waitQuit()
	// ensure evl will not be finalized until Serve returns
	runtime.SetFinalizer(e, nil)
	return err
}

// Shutdown signals a shutdown a begins server closing.
func (evl *eventLoop) Shutdown(ctx context.Context) error {
	//evl.Lock()
	//var svr = evl.svr
	//evl.svr = nil
	//evl.Unlock()
	//
	//if svr == nil {
	//	return nil
	//}
	evl.quit(nil)
	return evl.s.Close(ctx)
}

// waitQuit waits for a quit signal
func (evl *eventLoop) waitQuit() error {
	return <-evl.stop
}

func (evl *eventLoop) quit(err error) {
	select {
	case evl.stop <- err:
	default:
	}
}
