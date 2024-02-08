package znet

func init() {
	defaultPollerManager = new(pollerManager)
	defaultPollerManager.SetLoadBalancer(RoundRobin)
	defaultPollerManager.SetNumLoops(defaultNumLoops)
}

func Init(numLoops int) {
	defaultPollerManager.SetNumLoops(numLoops)
}
