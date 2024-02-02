package znet

import (
	"errors"
	"github.com/zhihanii/zlog"
	"net"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

type netFD struct {
	// file descriptor
	fd int
	// When calling netFD.dial(), fd will be registered into poll in some scenarios, such as dialing tcp socket,
	// but not in other scenarios, such as dialing unix socket.
	// This leads to a different behavior in register poller at after, so use this field to mark it.
	//pd *pollDesc
	// closed marks whether fd has expired
	closed uint32
	// Whether this is a streaming descriptor, as opposed to a
	// packet-based descriptor like a UDP socket. Immutable.
	isStream bool
	// Whether a zero byte read indicates EOF. This is false for a
	// message based socket connection.
	zeroReadIsEOF bool
	family        int    // AF_INET, AF_INET6, syscall.AF_UNIX
	sotype        int    // syscall.SOCK_STREAM, syscall.SOCK_DGRAM, syscall.SOCK_RAW
	isConnected   bool   // handshake completed or use of association with peer
	network       string // tcp tcp4 tcp6, udp, udp4, udp6, ip, ip4, ip6, unix, unixgram, unixpacket
	localAddr     net.Addr
	remoteAddr    net.Addr
}

func (c *netFD) Fd() (fd int) {
	return c.fd
}

// Read implements FDConn.
func (c *netFD) Read(b []byte) (n int, err error) {
	n, err = syscall.Read(c.fd, b)
	if err != nil {
		if err == syscall.EAGAIN || err == syscall.EINTR {
			return 0, nil
		}
	}
	return n, err
}

// Write implements FDConn.
func (c *netFD) Write(b []byte) (n int, err error) {
	n, err = syscall.Write(c.fd, b)
	if err != nil {
		if err == syscall.EAGAIN {
			return 0, nil
		}
	}
	return n, err
}

// Close will be executed only once.
func (c *netFD) Close() (err error) {
	if atomic.AddUint32(&c.closed, 1) != 1 {
		return nil
	}
	if c.fd > 0 {
		err = syscall.Close(c.fd)
		if err != nil {
			zlog.Errorf("netFD[%d] close error: %s", c.fd, err.Error())
		}
	}
	return err
}

// LocalAddr implements FDConn.
func (c *netFD) LocalAddr() (addr net.Addr) {
	return c.localAddr
}

// RemoteAddr implements FDConn.
func (c *netFD) RemoteAddr() (addr net.Addr) {
	return c.remoteAddr
}

// SetKeepAlive implements FDConn.
// TODO: only tcp conn is ok.
func (c *netFD) SetKeepAlive(second int) error {
	if !strings.HasPrefix(c.network, "tcp") {
		return nil
	}
	if second > 0 {
		return SetKeepAlive(c.fd, second)
	}
	return nil
}

func (c *netFD) SetDeadline(t time.Time) error {
	//return Exception(ErrUnsupported, "SetDeadline")
	return errors.New("unsupported SetDeadline")
}

// SetReadDeadline implements FDConn.
func (c *netFD) SetReadDeadline(t time.Time) error {
	//return Exception(ErrUnsupported, "SetReadDeadline")
	return errors.New("unsupported SetReadDeadline")
}

// SetWriteDeadline implements FDConn.
func (c *netFD) SetWriteDeadline(t time.Time) error {
	//return Exception(ErrUnsupported, "SetWriteDeadline")
	return errors.New("unsupported SetWriteDeadline")
}
