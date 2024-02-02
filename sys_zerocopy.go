package znet

import "syscall"

const (
	SO_ZEROCOPY       = 60
	SO_ZEROBLOCKTIMEO = 69
	MSG_ZEROCOPY      = 0x4000000
)

func setZeroCopy(fd int) error {
	return syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, SO_ZEROCOPY, 1)
}

func setBlockZeroCopySend(fd int, sec, usec int64) error {
	return syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, SO_ZEROBLOCKTIMEO, &syscall.Timeval{
		Sec:  sec,
		Usec: usec,
	})
}
