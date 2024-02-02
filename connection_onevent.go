package znet

import (
	"github.com/zhihanii/taskpool"
	"github.com/zhihanii/zlog"
)

type gracefulExit interface {
	isIdle() bool
	Close() error
}

func (c *connection) onRead() (needTrigger bool) {
	zlog.Infof("connection onRead")
	var onRead, ok = c.onReadCallback.Load().(OnRead)
	if !ok {
		return true
	}
	var processed = c.onProcess(
		func(c *connection) bool {
			return onRead != nil && c.Reader().Len() > 0
		},
		func(c *connection) {
			if onRead != nil {
				_ = onRead(c.ctx, c)
			}
		},
	)
	return !processed
}

func (c *connection) onProcess(isProcessable func(c *connection) bool, process func(c *connection)) (processed bool) {
	if process == nil {
		return false
	}
	if !c.lock(processing) {
		return false
	}

	var task = func() {
	START:
		// `process` must be executed at least once if `isProcessable` in order to cover the `send & close by peer` case.
		// Then the loop processing must ensure that the connection `IsActive`.
		if isProcessable(c) {
			process(c)
		}
		for c.IsActive() && isProcessable(c) {
			process(c)
		}
		// Handling callback if connection has been closed.
		if !c.IsActive() {
			c.closeCallback(false)
			return
		}
		c.unlock(processing)
		// Double check when exiting.
		if isProcessable(c) && c.lock(processing) {
			goto START
		}
		// task exits
		return
	}

	taskpool.Submit(c.ctx, task)

	return true
}

func (c *connection) isIdle() bool {
	return c.isUnlock(processing) &&
		c.inputBuffer.IsEmpty() &&
		c.outputBuffer.IsEmpty()
}
