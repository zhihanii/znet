package znet

import (
	"fmt"
	"sync/atomic"
)

type LoadBalance int8

const (
	RoundRobin LoadBalance = iota
)

type loadBalancer interface {
	LoadBalance() LoadBalance
	Pick() Poller
	Rebalance(pollers []Poller)
}

func newLoadBalancer(lb LoadBalance, pollers []Poller) (loadBalancer, error) {
	switch lb {
	case RoundRobin:
		return newRoundRobinLoadBalancer(pollers), nil
	default:
		return nil, fmt.Errorf("not supported loadbalance: %d", lb)
	}
}

func newRoundRobinLoadBalancer(pollers []Poller) loadBalancer {
	return &roundRobinLoadBalancer{
		pollers: pollers,
		size:    len(pollers),
	}
}

type roundRobinLoadBalancer struct {
	pollers []Poller
	cur     int32
	size    int
}

func (b *roundRobinLoadBalancer) LoadBalance() LoadBalance {
	return RoundRobin
}

func (b *roundRobinLoadBalancer) Pick() (poller Poller) {
	idx := int(atomic.AddInt32(&b.cur, 1)) % b.size
	return b.pollers[idx]
}

func (b *roundRobinLoadBalancer) Rebalance(pollers []Poller) {
	b.pollers, b.size = pollers, len(pollers)
}
