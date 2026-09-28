package app

import (
	"time"

	"dae-tui/internal/driver"
)

// latHistory accumulates a rolling window of latency samples per node.
// nodeLatencies only ever carries the latest value, so "this node has been
// getting slower for ten minutes" is invisible in a single number; the
// history is built client-side from the poll stream. Memory is bounded by
// capping the tracked nodes (LRU) and the samples kept per node.
type latHistory struct {
	window   int // samples kept per node
	maxNodes int // nodes tracked before the least recently seen is evicted

	order  []string // node IDs, least recently updated first
	byNode map[string][]latSample
}

type latSample struct {
	ms    int
	alive bool
	at    time.Time
}

// The history budget: samples kept per node (~3 minutes at the 3s poll
// cadence) and how many nodes stay tracked before the least recently seen
// is evicted.
const (
	latWindow   = 60
	latMaxNodes = 256
)

func newLatHistory() *latHistory {
	return &latHistory{
		window:   latWindow,
		maxNodes: latMaxNodes,
		byNode:   map[string][]latSample{},
	}
}

// add appends a latency reading, trimming the node's window and evicting the
// least recently updated node when the tracked set is full.
func (h *latHistory) add(l driver.Latency) {
	if h.byNode == nil {
		h.byNode = map[string][]latSample{}
	}
	if _, ok := h.byNode[l.NodeID]; ok {
		h.touch(l.NodeID)
	} else {
		for len(h.order) >= h.maxNodes {
			oldest := h.order[0]
			h.order = h.order[1:]
			delete(h.byNode, oldest)
		}
	}
	h.order = append(h.order, l.NodeID)
	samples := append(h.byNode[l.NodeID], latSample{ms: l.Ms, alive: l.Alive, at: l.TestedAt})
	if len(samples) > h.window {
		samples = samples[len(samples)-h.window:]
	}
	h.byNode[l.NodeID] = samples
}

// touch moves a node to the most-recently-updated end of the order.
func (h *latHistory) touch(id string) {
	for i, v := range h.order {
		if v == id {
			h.order = append(h.order[:i], h.order[i+1:]...)
			return
		}
	}
}

// span is the time between a node's oldest and newest kept sample — the
// window the trend chart actually covers. It reads the raw window rather
// than the alive-only series, which is shorter whenever probes timed out.
func (h *latHistory) span(nodeID string) time.Duration {
	s := h.byNode[nodeID]
	if len(s) < 2 {
		return 0
	}
	return s[len(s)-1].at.Sub(s[0].at)
}

// series renders a node's successful probes as a sparkline series. Dead
// probes are skipped, so a timeout reads as a gap in the line rather than as
// an impossibly fast node.
func (h *latHistory) series(nodeID string) []float64 {
	samples := h.byNode[nodeID]
	if len(samples) == 0 {
		return nil
	}
	out := make([]float64, 0, len(samples))
	for _, s := range samples {
		if s.alive && s.ms > 0 {
			out = append(out, float64(s.ms))
		}
	}
	return out
}
