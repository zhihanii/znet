package znet

import (
	"fmt"
	"github.com/zhihanii/zlog"
)

const defaultNumLoops = 100

var defaultPollerManager *pollerManager

type pollerManager struct {
	numLoops int
	pollers  []Poller
	balancer loadBalancer
}

func (m *pollerManager) SetNumLoops(numLoops int) error {
	if numLoops < 1 {
		return fmt.Errorf("set invalid numLoops[%d]", numLoops)
	}

	if numLoops < m.numLoops {
		var err error
		var pollers = make([]Poller, numLoops)
		for i := 0; i < m.numLoops; i++ {
			if i < numLoops {
				pollers[i] = m.pollers[i]
			} else {
				if err = m.pollers[i].Close(); err != nil {
					zlog.Errorf("poller close failed: %v", err)
				}
			}
		}
		m.numLoops = numLoops
		m.pollers = pollers
		m.balancer.Rebalance(m.pollers)
		return nil
	}

	m.numLoops = numLoops
	return m.buildPollers()
}

func (m *pollerManager) SetLoadBalancer(lb LoadBalance) error {
	if m.balancer != nil && m.balancer.LoadBalance() == lb {
		return nil
	}
	m.balancer, _ = newLoadBalancer(lb, m.pollers)
	return nil
}

func (m *pollerManager) Reset() error {
	for _, p := range m.pollers {
		p.Close()
	}
	m.pollers = nil
	return m.buildPollers()
}

func (m *pollerManager) Pick() Poller {
	return m.balancer.Pick()
}

func (m *pollerManager) buildPollers() error {
	for i := len(m.pollers); i < m.numLoops; i++ {
		var p = openPoller()
		m.pollers = append(m.pollers, p)
		go p.Poll()
	}
	m.balancer.Rebalance(m.pollers)
	return nil
}
