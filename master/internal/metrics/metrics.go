// Package metrics keeps in-memory metric history for nodes and instances.
package metrics

import (
	"sync"
	"time"
)

// Point is one metrics sample.
type Point struct {
	T int64   `json:"t"` // unix ms
	A float64 `json:"a"` // cpu percent
	B float64 `json:"b"` // bytes used (node: mem used, instance: rss)
}

// maxPoints per series (~20 min at 2s sampling).
const maxPoints = 600

// Store holds metric history.
type Store struct {
	mu       sync.Mutex
	node     map[int64][]Point
	inst     map[int64][]Point
	nodeSums map[int64]nodeSummary
}

type nodeSummary struct {
	MemTotal uint64
	MemUsed  uint64
	CPU      float64
	Uptime   uint64
}

// NewStore creates an empty store.
func NewStore() *Store {
	return &Store{
		node:     map[int64][]Point{},
		inst:     map[int64][]Point{},
		nodeSums: map[int64]nodeSummary{},
	}
}

// PushNode records a node sample.
func (s *Store) PushNode(nodeID int64, cpu float64, memUsed float64, at time.Time) {
	s.push(s.node, nodeID, at, cpu, memUsed)
}

// PushInstance records an instance sample.
func (s *Store) PushInstance(instanceID int64, cpu float64, rss float64, at time.Time) {
	s.push(s.inst, instanceID, at, cpu, rss)
}

func (s *Store) push(m map[int64][]Point, id int64, at time.Time, a, b float64) {
	p := Point{T: at.UnixMilli(), A: a, B: b}
	s.mu.Lock()
	defer s.mu.Unlock()
	series := append(m[id], p)
	if len(series) > maxPoints {
		series = series[len(series)-maxPoints:]
	}
	m[id] = series
}

// NodeHistory returns the stored node samples.
func (s *Store) NodeHistory(nodeID int64) []Point {
	return s.get(s.node, nodeID)
}

// InstanceHistory returns the stored instance samples.
func (s *Store) InstanceHistory(instanceID int64) []Point {
	return s.get(s.inst, instanceID)
}

func (s *Store) get(m map[int64][]Point, id int64) []Point {
	s.mu.Lock()
	defer s.mu.Unlock()
	src := m[id]
	out := make([]Point, len(src))
	copy(out, src)
	return out
}

// DropInstance forgets history for a removed instance.
func (s *Store) DropInstance(instanceID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.inst, instanceID)
}

// DropNode forgets history for a removed node.
func (s *Store) DropNode(nodeID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.node, nodeID)
	delete(s.nodeSums, nodeID)
}

// SetNodeSummary stores the latest point-in-time node totals, which are not
// part of the per-sample series (total RAM, uptime).
func (s *Store) SetNodeSummary(nodeID int64, memTotal, memUsed uint64, cpu float64, uptime uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodeSums[nodeID] = nodeSummary{MemTotal: memTotal, MemUsed: memUsed, CPU: cpu, Uptime: uptime}
}

// NodeSummary returns the latest node totals.
func (s *Store) NodeSummary(nodeID int64) (memTotal, memUsed uint64, cpu float64, uptime uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sum := s.nodeSums[nodeID]
	return sum.MemTotal, sum.MemUsed, sum.CPU, sum.Uptime
}
