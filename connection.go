package znet

import (
	"context"
	"fmt"
	"github.com/zhihanii/zlog"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type Conn interface {
	FDConn
	ReadLine() ([]byte, bool, error)
	WriteString(s string) (n int, err error)
	Flush() error
	LoadValue() any
	StoreValue(v any)
}

type FDConn interface {
	net.Conn
	Fd() int
}

const (
	defaultZeroCopyTimeoutSec = 60
)

type connection struct {
	netFD
	locker

	onReadCallback atomic.Value

	ctx             context.Context
	closeCallbacks  atomic.Value
	operator        *FDOperator
	readTimeout     time.Duration
	readTimer       *time.Timer
	readTrigger     chan struct{}
	waitReadSize    int64
	writeTrigger    chan error
	inputBuffer     *LinkBuffer
	outputBuffer    *LinkBuffer
	inputBarrier    *barrier
	outputBarrier   *barrier
	supportZeroCopy bool
	maxSize         int // The maximum size of data between two Release().
	bookSize        int // The size of data that can be read at once.

	value     any
	lastFlush time.Time
}

func (c *connection) Reader() Reader {
	return c
}

func (c *connection) Writer() Writer {
	return c
}

func (c *connection) LoadValue() any {
	return c.value
}

func (c *connection) StoreValue(v any) {
	c.value = v
}

// IsActive implements Connection.
func (c *connection) IsActive() bool {
	return c.isCloseBy(none)
}

func (c *connection) SetOnRead(onRead func(context.Context, Conn) error) {
	c.onReadCallback.Store(OnRead(onRead))
}

// SetIdleTimeout implements Connection.
func (c *connection) SetIdleTimeout(timeout time.Duration) error {
	if timeout > 0 {
		return c.SetKeepAlive(int(timeout.Seconds()))
	}
	return nil
}

// SetReadTimeout implements Connection.
func (c *connection) SetReadTimeout(timeout time.Duration) error {
	if timeout >= 0 {
		c.readTimeout = timeout
	}
	return nil
}

// Next implements Connection.
func (c *connection) Next(n int) (p []byte, err error) {
	if err = c.waitRead(n); err != nil {
		return p, err
	}
	return c.inputBuffer.Next(n)
}

// Peek implements Connection.
func (c *connection) Peek(n int) (buf []byte, err error) {
	if err = c.waitRead(n); err != nil {
		return buf, err
	}
	return c.inputBuffer.Peek(n)
}

// Skip implements Connection.
func (c *connection) Skip(n int) (err error) {
	if err = c.waitRead(n); err != nil {
		return err
	}
	return c.inputBuffer.Skip(n)
}

// Release implements Connection.
func (c *connection) Release() (err error) {
	// Check inputBuffer length first to reduce contention in mux situation.
	// c.operator.do competes with c.inputs/c.inputAck
	if c.inputBuffer.Len() == 0 && c.operator.tryOnEvent() {
		maxSize := c.inputBuffer.calcMaxSize()
		// Set the maximum value of maxsize equal to mallocMax to prevent GC pressure.
		if maxSize > mallocMax {
			maxSize = mallocMax
		}

		if maxSize > c.maxSize {
			c.maxSize = maxSize
		}
		// Double check length to reset tail node
		if c.inputBuffer.Len() == 0 {
			c.inputBuffer.resetTail(c.maxSize)
		}
		c.operator.done()
	}
	return c.inputBuffer.Release()
}

// Slice implements Connection.
func (c *connection) Slice(n int) (r Reader, err error) {
	if err = c.waitRead(n); err != nil {
		return nil, err
	}
	return c.inputBuffer.Slice(n)
}

// Len implements Connection.
func (c *connection) Len() (length int) {
	return c.inputBuffer.Len()
}

// Until implements Connection.
func (c *connection) Until(delim byte) (line []byte, err error) {
	var n, l int
	for {
		if err = c.waitRead(n + 1); err != nil {
			// return all the data in the buffer
			line, _ = c.inputBuffer.Next(c.inputBuffer.Len())
			return
		}

		l = c.inputBuffer.Len()
		i := c.inputBuffer.indexByte(delim, n)
		if i < 0 {
			n = l //skip all exists bytes
			continue
		}
		return c.Next(i + 1)
	}
}

func (c *connection) ReadSlice(delim byte) (line []byte, err error) {
	var n, l int
	for {
		if err = c.waitRead(n + 1); err != nil {
			// return all the data in the buffer
			line, _ = c.inputBuffer.Next(c.inputBuffer.Len())
			return
		}

		l = c.inputBuffer.Len()
		i := c.inputBuffer.indexByte(delim, n)
		if i < 0 {
			n = l //skip all exists bytes
			continue
		}
		return c.Next(i + 1)
	}
}

func (c *connection) ReadLine() (line []byte, isPrefix bool, err error) {
	line, err = c.ReadSlice('\n')
	if len(line) == 0 {
		if err != nil {
			line = nil
		}
		return
	}
	err = nil

	if line[len(line)-1] == '\n' {
		drop := 1
		if len(line) > 1 && line[len(line)-2] == '\r' {
			drop = 2
		}
		line = line[:len(line)-drop]
	}
	return
}

// ReadString implements Connection.
func (c *connection) ReadString(n int) (s string, err error) {
	if err = c.waitRead(n); err != nil {
		return s, err
	}
	return c.inputBuffer.ReadString(n)
}

// ReadBinary implements Connection.
func (c *connection) ReadBinary(n int) (p []byte, err error) {
	if err = c.waitRead(n); err != nil {
		return p, err
	}
	return c.inputBuffer.ReadBinary(n)
}

func (c *connection) ReadBytes(n int) (p []byte, err error) {
	if err = c.waitRead(n); err != nil {
		return p, err
	}
	return c.inputBuffer.ReadBytes(n)
}

// ReadByte implements Connection.
func (c *connection) ReadByte() (b byte, err error) {
	if err = c.waitRead(1); err != nil {
		return b, err
	}
	return c.inputBuffer.ReadByte()
}

// ------------------------------------------ implement zero-copy writer ------------------------------------------

// Malloc implements Connection.
func (c *connection) Malloc(n int) (buf []byte, err error) {
	return c.outputBuffer.Malloc(n)
}

// MallocLen implements Connection.
func (c *connection) MallocLen() (length int) {
	return c.outputBuffer.MallocLen()
}

// Flush will send all malloc data to the peer,
// so must confirm that the allocated bytes have been correctly assigned.
//
// Flush first checks whether the out buffer is empty.
// If empty, it will call syscall.Write to send data directly,
// otherwise the buffer will be sent asynchronously by the epoll trigger.
func (c *connection) Flush() error {
	if !c.IsActive() || !c.lock(flushing) {
		return fmt.Errorf("err conn closed when flush")
	}
	defer c.unlock(flushing)
	c.outputBuffer.Flush()
	return c.flush()
}

// MallocAck implements Connection.
func (c *connection) MallocAck(n int) (err error) {
	return c.outputBuffer.MallocAck(n)
}

// Append implements Connection.
func (c *connection) Append(w Writer) (err error) {
	return c.outputBuffer.Append(w)
}

// WriteString implements Connection.
func (c *connection) WriteString(s string) (n int, err error) {
	return c.outputBuffer.WriteString(s)
}

// WriteBinary implements Connection.
func (c *connection) WriteBinary(b []byte) (n int, err error) {
	return c.outputBuffer.WriteBinary(b)
}

func (c *connection) WriteBytes(b []byte) (n int, err error) {
	return c.outputBuffer.WriteBytes(b)
}

// WriteDirect implements Connection.
func (c *connection) WriteDirect(p []byte, remainCap int) (err error) {
	return c.outputBuffer.WriteDirect(p, remainCap)
}

// WriteByte implements Connection.
func (c *connection) WriteByte(b byte) (err error) {
	return c.outputBuffer.WriteByte(b)
}

// ------------------------------------------ implement net.Conn ------------------------------------------

// Read behavior is the same as net.Conn, it will return io.EOF if buffer is empty.
func (c *connection) Read(p []byte) (n int, err error) {
	l := len(p)
	if l == 0 {
		return 0, nil
	}
	if err = c.waitRead(1); err != nil {
		return 0, err
	}
	if has := c.inputBuffer.Len(); has < l {
		l = has
	}
	src, err := c.inputBuffer.Next(l)
	n = copy(p, src)
	if err == nil {
		err = c.inputBuffer.Release()
	}
	return n, err
}

// Write will Flush soon.
func (c *connection) Write(p []byte) (n int, err error) {
	//atomic实现的加锁
	if !c.IsActive() || !c.lock(flushing) {
		return 0, fmt.Errorf("err conn closed when write")
	}
	defer c.unlock(flushing)

	dst, _ := c.outputBuffer.Malloc(len(p))
	n = copy(dst, p)
	//c.outputBuffer.Flush()
	//err = c.flush()
	now := time.Now()
	if now.Sub(c.lastFlush) > time.Millisecond*1500 {
		c.lastFlush = now
		c.outputBuffer.Flush()
		err = c.flush()
	}
	return n, err
}

// Close implements Connection.
func (c *connection) Close() error {
	return c.onClose()
}

// ------------------------------------------ private ------------------------------------------

var barrierPool = sync.Pool{
	New: func() interface{} {
		return &barrier{
			bs:  make([][]byte, barriercap),
			ivs: make([]syscall.Iovec, barriercap),
		}
	},
}

// init initialize the connection with options
func (c *connection) init(conn FDConn, opts *options, eh EventHandler) (err error) {
	// init buffer, barrier, finalizer
	c.readTrigger = make(chan struct{}, 1)
	c.writeTrigger = make(chan error, 1)
	c.bookSize, c.maxSize = block1k/2, pageSize
	c.inputBuffer, c.outputBuffer = NewLinkBuffer(pageSize), NewLinkBuffer()
	c.inputBarrier, c.outputBarrier = barrierPool.Get().(*barrier), barrierPool.Get().(*barrier)

	c.initNetFD(conn) // conn must be *netFD{}
	c.initFDOperator()
	c.initFinalizer()

	syscall.SetNonblock(c.fd, true)
	// enable TCP_NODELAY by default
	switch c.network {
	case "tcp", "tcp4", "tcp6":
		setTCPNoDelay(c.fd, true)
	}
	// check zero-copy
	//if setZeroCopy(c.fd) == nil && setBlockZeroCopySend(c.fd, defaultZeroCopyTimeoutSec, 0) == nil {
	//	zlog.Infof("support zero-copy")
	//	c.supportZeroCopy = true
	//}

	err1 := setZeroCopy(c.fd)
	//err2 := setBlockZeroCopySend(c.fd, defaultZeroCopyTimeoutSec, 0)
	if err1 == nil {
		//zlog.Infof("support zero-copy")
		c.supportZeroCopy = true
	}

	// connection initialized and prepare options
	return c.onPrepare(opts, eh)
}

func (c *connection) initNetFD(conn FDConn) {
	if nfd, ok := conn.(*netFD); ok {
		c.netFD = *nfd
		return
	}
	c.netFD = netFD{
		fd:         conn.Fd(),
		localAddr:  conn.LocalAddr(),
		remoteAddr: conn.RemoteAddr(),
	}
}

func (c *connection) initFDOperator() {
	op := allocOp() //获取一个FDOperator来使用
	op.FD = c.fd
	op.OnHup = c.onHup
	op.Inputs, op.InputAck = c.inputs, c.inputAck
	op.Outputs, op.OutputAck = c.outputs, c.outputAck
	op.isConnection = true

	// if connection has been registered, must reuse poll here.
	//if c.pd != nil && c.pd.operator != nil {
	//	op.poll = c.pd.operator.poll
	//}
	c.operator = op
}

func (c *connection) initFinalizer() {
	c.AddCloseCallback(func(c *connection) error {
		c.stop(flushing)
		// stop the finalizing state to prevent conn.fill function to be performed
		c.stop(finalizing)
		freeOp(c.operator)
		c.netFD.Close()
		c.closeBuffer()
		return nil
	})
}

func (c *connection) triggerRead() {
	select {
	case c.readTrigger <- struct{}{}:
	default:
	}
}

func (c *connection) triggerWrite(err error) {
	select {
	case c.writeTrigger <- err:
	default:
	}
}

// waitRead will wait full n bytes.
func (c *connection) waitRead(n int) (err error) {
	if n <= c.inputBuffer.Len() {
		return nil
	}
	atomic.StoreInt64(&c.waitReadSize, int64(n))
	defer atomic.StoreInt64(&c.waitReadSize, 0)
	if c.readTimeout > 0 {
		return c.waitReadWithTimeout(n)
	}
	// wait full n
	for c.inputBuffer.Len() < n {
		if c.IsActive() {
			<-c.readTrigger
			continue
		}
		// confirm that fd is still valid.
		if atomic.LoadUint32(&c.netFD.closed) == 0 {
			return c.fill(n)
		}
		return fmt.Errorf("err conn closed wait read")
	}
	return nil
}

// waitReadWithTimeout will wait full n bytes or until timeout.
func (c *connection) waitReadWithTimeout(n int) (err error) {
	// set read timeout
	if c.readTimer == nil {
		c.readTimer = time.NewTimer(c.readTimeout)
	} else {
		c.readTimer.Reset(c.readTimeout)
	}

	for c.inputBuffer.Len() < n {
		if !c.IsActive() {
			// cannot return directly, stop timer before !
			// confirm that fd is still valid.
			if atomic.LoadUint32(&c.netFD.closed) == 0 {
				err = c.fill(n)
			} else {
				err = fmt.Errorf("err conn closed wait read")
			}
			break
		}

		select {
		case <-c.readTimer.C:
			// double check if there is enough data to be read
			if c.inputBuffer.Len() >= n {
				return nil
			}
			return fmt.Errorf("read timeout remote addr: %s", c.remoteAddr.String())
		case <-c.readTrigger:
			continue
		}
	}

	// clean timer.C
	if !c.readTimer.Stop() {
		<-c.readTimer.C
	}
	return err
}

// fill data after connection is closed.
func (c *connection) fill(need int) (err error) {
	//atomic实现的加锁
	if !c.lock(finalizing) {
		return fmt.Errorf("err conn closed")
	}
	defer c.unlock(finalizing)

	var n int
	for {
		//从内核缓冲区读取数据到inputBuffer
		n, err = readv(c.fd, c.inputs(c.inputBarrier.bs), c.inputBarrier.ivs)
		c.inputAck(n)
		err = c.eofError(n, err)
		if err != nil {
			break
		}
	}
	if c.inputBuffer.Len() >= need {
		return nil
	}
	return err
}

func (c *connection) eofError(n int, err error) error {
	if err == syscall.EINTR {
		return nil
	}
	if n == 0 && err == nil {
		return fmt.Errorf("eof")
	}
	return err
}

func (c *connection) onPrepare(opts *options, eh EventHandler) (err error) {
	c.SetOnRead(eh.OnRead)

	if c.ctx == nil {
		c.ctx = context.Background()
	}
	if c.IsActive() {
		return c.register()
	}
	return nil
}

// closeCallback .
// It can be confirmed that closeCallback and onRequest will not be executed concurrently.
// If onRequest is still running, it will trigger closeCallback on exit.
func (c *connection) closeCallback(needLock bool) (err error) {
	if needLock && !c.lock(processing) {
		return nil
	}
	// If Close is called during OnPrepare, poll is not registered.
	if c.closeBy(user) && c.operator.poller != nil {
		c.operator.Control(EpollDetach)
	}
	var latest = c.closeCallbacks.Load()
	if latest == nil {
		return nil
	}
	for node := latest.(*closeCallbackNode); node != nil; node = node.pre {
		node.cb(c)
	}
	return nil
}

func (c *connection) register() (err error) {
	if c.operator.poller != nil {
		err = c.operator.Control(EpollModRead)
	} else {
		c.operator.poller = defaultPollerManager.Pick()
		err = c.operator.Control(EpollRead)
	}
	if err != nil {
		zlog.Errorf("connection register failed: %v", err)
		c.Close()
		return
	}
	return nil
}

type CloseCallback func(c *connection) error

type closeCallbackNode struct {
	cb  CloseCallback
	pre *closeCallbackNode
}

func (c *connection) AddCloseCallback(callback CloseCallback) error {
	if callback == nil {
		return nil
	}
	var node = &closeCallbackNode{
		cb: callback,
	}
	if pre := c.closeCallbacks.Load(); pre != nil {
		node.pre = pre.(*closeCallbackNode)
	}
	c.closeCallbacks.Store(node)
	return nil
}

//func (c *connection) GetWebsocketConn() *websocket.Conn {
//	return c.wsConn
//}
//
//func (c *connection) SetWebsocketConn(wsConn *websocket.Conn) {
//	c.wsConn = wsConn
//}

// onHup means close by poller.
// 用于该connection的fd在遇到错误情况时(poller在执行相关操作时产生错误、监听到错误事件等)
// 释放相关资源并close
func (c *connection) onHup(p Poller) error {
	if c.closeBy(poller) {
		c.triggerRead()
		c.triggerWrite(fmt.Errorf("err conn closed"))
		// It depends on closing by user if OnConnect and OnRequest is nil, otherwise it needs to be released actively.
		// It can be confirmed that the OnRequest goroutine has been exited before closecallback executing,
		// and it is safe to close the buffer at this time.
		//var onConnect, _ = c.onConnectCallback.Load().(OnConnect)
		var onRead, _ = c.onReadCallback.Load().(OnRead)
		if onRead != nil {
			c.closeCallback(true)
		}
	}
	return nil
}

// onClose means close by user.
// 用户主动调用了Close方法而执行close
func (c *connection) onClose() error {
	if c.closeBy(user) {
		c.triggerRead()
		c.triggerWrite(fmt.Errorf("err conn closed"))
		c.closeCallback(true)
		return nil
	}
	if c.isCloseBy(poller) {
		// Connection with OnRequest of nil
		// relies on the user to actively close the connection to recycle resources.
		c.closeCallback(true)
	}
	return nil
}

// closeBuffer recycle input & output LinkBuffer.
func (c *connection) closeBuffer() {
	//var onConnect, _ = c.onConnectCallback.Load().(OnConnect)
	var onRead, _ = c.onReadCallback.Load().(OnRead)
	if c.inputBuffer.Len() == 0 || onRead != nil {
		c.inputBuffer.Close()
		barrierPool.Put(c.inputBarrier)
	}

	c.outputBuffer.Close()
	barrierPool.Put(c.outputBarrier)
}

// inputs implements FDOperator.
func (c *connection) inputs(vs [][]byte) (rs [][]byte) {
	//从buffer中获取一段[]byte
	vs[0] = c.inputBuffer.book(c.bookSize, c.maxSize)
	return vs[:1]
}

// inputAck implements FDOperator.
func (c *connection) inputAck(n int) (err error) {
	if n <= 0 {
		c.inputBuffer.bookAck(0)
		return nil
	}

	// Auto size bookSize.
	if n == c.bookSize && c.bookSize < mallocMax {
		c.bookSize <<= 1
	}

	length, _ := c.inputBuffer.bookAck(n)
	if c.maxSize < length {
		c.maxSize = length
	}
	if c.maxSize > mallocMax {
		c.maxSize = mallocMax
	}

	var needTrigger = true
	if length == n {
		needTrigger = c.onRead()
	}
	if needTrigger && length >= int(atomic.LoadInt64(&c.waitReadSize)) {
		c.triggerRead()
	}
	return nil
}

// outputs implements FDOperator.
func (c *connection) outputs(vs [][]byte) (rs [][]byte, supportZeroCopy bool) {
	if c.outputBuffer.IsEmpty() {
		c.rw2r()
		return rs, c.supportZeroCopy
	}
	rs = c.outputBuffer.GetBytes(vs)
	return rs, c.supportZeroCopy
}

// outputAck implements FDOperator.
func (c *connection) outputAck(n int) (err error) {
	if n > 0 {
		c.outputBuffer.Skip(n)
		c.outputBuffer.Release()
	}
	if c.outputBuffer.IsEmpty() {
		c.rw2r()
	}
	return nil
}

// rw2r removed the monitoring of write events.
func (c *connection) rw2r() {
	c.operator.Control(EpollRW2R)
	c.triggerWrite(nil)
}

// flush write data directly.
func (c *connection) flush() error {
	if c.outputBuffer.IsEmpty() {
		return nil
	}
	// TODO: Let the upper layer pass in whether to use ZeroCopy.
	var bs = c.outputBuffer.GetBytes(c.outputBarrier.bs)
	var n, err = sendmsg(c.fd, bs, c.outputBarrier.ivs, false && c.supportZeroCopy)
	if err != nil && err != syscall.EAGAIN {
		return fmt.Errorf("flush: %v", err)
	}
	if n > 0 {
		err = c.outputBuffer.Skip(n)
		c.outputBuffer.Release()
		if err != nil {
			return fmt.Errorf("flush: %v", err)
		}
	}
	// return if write all buffer.
	if c.outputBuffer.IsEmpty() {
		return nil
	}
	err = c.operator.Control(EpollR2RW)
	if err != nil {
		return fmt.Errorf("flush: %v", err)
	}
	err = <-c.writeTrigger
	if err != nil {
		return fmt.Errorf("flush: %v", err)
	}
	return nil
}
