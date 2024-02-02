package znet

import "context"

type OnConnect func(ctx context.Context, conn Conn) error
type OnRead func(ctx context.Context, conn Conn) error

type EventHandler interface {
	OnConnect(ctx context.Context, conn Conn) error
	OnRead(ctx context.Context, conn Conn) error
}
