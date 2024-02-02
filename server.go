package znet

import (
	"context"
	"github.com/zhihanii/im-pusher/pkg/gopool"
	"github.com/zhihanii/zlog"
	"strings"
	"sync"
	"time"
)

func newServer(listener Listener, eh EventHandler, opts *options) *server {
	return &server{
		o:        opts,
		eh:       eh,
		listener: listener,
	}
}

type server struct {
	o           *options
	eh          EventHandler
	operator    *FDOperator
	listener    Listener
	connections sync.Map
}

func (s *server) Run() (err error) {
	s.operator = &FDOperator{
		FD: s.listener.Fd(),
	}
	s.operator.poller = defaultPollerManager.Pick()
	err = s.operator.Control(EpollRead)
	if err != nil {

	}
	return err
}

func (s *server) Close(ctx context.Context) error {
	s.operator.Control(EpollDetach)
	s.listener.Close()

	var ticker = time.NewTicker(time.Second)
	defer ticker.Stop()
	var hasConn bool
	for {
		hasConn = false
		s.connections.Range(func(key, value interface{}) bool {
			var conn, ok = value.(gracefulExit)
			if !ok || conn.isIdle() {
				value.(Conn).Close()
			}
			hasConn = true
			return true
		})
		if !hasConn { // all connections have been closed
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			continue
		}
	}
}

func (s *server) OnAccept() error {
	conn, err := s.listener.Accept()
	if err != nil {
		// shut down
		if strings.Contains(err.Error(), "closed") {
			//s.operator.Control(PollDetach)
			//s.onQuit(err)
			return err
		}
		zlog.Errorf("accept connection failed: %v", err)
		return err
	}
	if conn == nil {
		return nil
	}

	var c = new(connection)
	c.init(conn.(FDConn), s.o, s.eh)
	if !c.IsActive() {
		return nil
	}
	var fd = conn.(FDConn).Fd()
	c.AddCloseCallback(func(c *connection) error {
		s.connections.Delete(fd)
		return nil
	})
	s.connections.Store(fd, c)

	gopool.Submit(c.ctx, func() {
		s.eh.OnConnect(c.ctx, c)
	})

	//todo 关闭连接前可以先返回错误信息

	return nil
}
