package znet

import (
	"errors"
	"net"
	"os"
	"syscall"
)

type Listener interface {
	net.Listener
	Fd() int
}

func ConvertListener(netListener net.Listener) (Listener, error) {
	if tmp, ok := netListener.(Listener); ok {
		return tmp, nil
	}
	l := new(listener)
	l.netListener = netListener
	l.addr = netListener.Addr()
	var err = l.parseFD()
	if err != nil {
		return nil, err
	}
	return l, syscall.SetNonblock(l.fd, true)
}

type listener struct {
	fd          int
	addr        net.Addr
	netListener net.Listener
	file        *os.File
}

func (l *listener) Accept() (net.Conn, error) {
	var fd, sa, err = syscall.Accept(l.fd)
	if err != nil {
		if err == syscall.EAGAIN {
			return nil, nil
		}
		return nil, err
	}
	var nfd = new(netFD)
	nfd.fd = fd
	nfd.localAddr = l.addr
	nfd.network = l.addr.Network()
	nfd.remoteAddr = sockaddrToAddr(sa)
	return nfd, nil
}

func (l *listener) Close() error {
	if l.fd != 0 {
		syscall.Close(l.fd)
	}
	if l.file != nil {
		l.file.Close()
	}
	if l.netListener != nil {
		l.netListener.Close()
	}
	return nil
}

func (l *listener) Addr() net.Addr {
	return l.addr
}

func (l *listener) Fd() int {
	return l.fd
}

func (l *listener) parseFD() (err error) {
	switch netListener := l.netListener.(type) {
	case *net.TCPListener:
		l.file, err = netListener.File()
	default:
		return errors.New("listener type can't support")
	}
	if err != nil {
		return err
	}
	l.fd = int(l.file.Fd())
	return nil
}
