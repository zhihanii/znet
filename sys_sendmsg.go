package znet

import (
	"syscall"
	"unsafe"
)

func sendmsg(fd int, bs [][]byte, ivs []syscall.Iovec, zerocopy bool) (n int, err error) {
	iovLen := iovecs(bs, ivs)
	if iovLen == 0 {
		return 0, nil
	}
	var msghdr = syscall.Msghdr{
		Iov:    &ivs[0],
		Iovlen: uint64(iovLen),
	}
	var flags uintptr
	if zerocopy {
		flags = MSG_ZEROCOPY
	}
	r, _, e := syscall.RawSyscall(syscall.SYS_SENDMSG, uintptr(fd), uintptr(unsafe.Pointer(&msghdr)), flags)
	resetIovecs(bs, ivs[:iovLen])
	if e != 0 {
		return int(r), syscall.Errno(e)
	}
	return int(r), nil
}
