// Package metrics collects node and per-instance metrics.
package metrics

import (
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/process"

	"panel/common/protocol"
)

// Interval between samples.
const Interval = 2 * time.Second

// Collector samples node + instance metrics and emits node.metrics events.
type Collector struct {
	runner instanceSource
	emit   func(protocol.Message)
	stop   chan struct{}
	once   sync.Once
}

// instanceSource is the runner subset needed here.
type instanceSource interface {
	RunningInstanceIDs() []int64
	PIDOf(instanceID int64) int
}

// NewCollector creates a collector.
func NewCollector(runner instanceSource, emit func(protocol.Message)) *Collector {
	return &Collector{runner: runner, emit: emit, stop: make(chan struct{})}
}

// Start runs the sampling loop until Stop.
func (c *Collector) Start() {
	// Prime gopsutil cpu counters; the first Percent call is meaningless.
	_, _ = cpu.Percent(0, false)
	t := time.NewTicker(Interval)
	go func() {
		defer t.Stop()
		for {
			select {
			case <-c.stop:
				return
			case <-t.C:
				c.sample()
			}
		}
	}()
}

// Stop terminates the loop.
func (c *Collector) Stop() { c.once.Do(func() { close(c.stop) }) }

// Snapshot returns one metrics sample.
func (c *Collector) Snapshot() protocol.MetricsSnapshot {
	return c.sample()
}

func (c *Collector) sample() protocol.MetricsSnapshot {
	snap := protocol.MetricsSnapshot{}
	if cpuPct, err := cpu.Percent(0, false); err == nil && len(cpuPct) == 1 {
		snap.CPU = round1(cpuPct[0])
	}
	if vm, err := mem.VirtualMemory(); err == nil {
		snap.MemTotal = vm.Total
		snap.MemUsed = vm.Used
	}
	for _, id := range c.runner.RunningInstanceIDs() {
		pid := c.runner.PIDOf(id)
		if pid == 0 {
			continue
		}
		p, err := process.NewProcess(int32(pid))
		if err != nil {
			continue
		}
		im := protocol.InstanceMetric{InstanceID: id}
		if pct, err := p.CPUPercent(); err == nil {
			im.CPU = round1(pct)
		}
		if mi, err := p.MemoryInfo(); err == nil {
			im.RSS = mi.RSS
		}
		snap.Instances = append(snap.Instances, im)
	}
	if c.emit != nil {
		c.emit(protocol.NewEvent(protocol.EventNodeMetrics, protocol.NodeMetricsEvent{
			CPU:       snap.CPU,
			MemTotal:  snap.MemTotal,
			MemUsed:   snap.MemUsed,
			Instances: snap.Instances,
		}))
	}
	return snap
}

func round1(v float64) float64 {
	return float64(int(v*10+0.5)) / 10
}
