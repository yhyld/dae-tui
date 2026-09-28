package app

import (
	"dae-tui/internal/driver"
)

// trafficPage carries the traffic snapshot the home page renders into its
// own boxes. It once had a View of its own (a standalone chart layout);
// the home grid took over rendering and that layout math died with it.
type trafficPage struct {
	snap   driver.TrafficSnapshot
	err    error
	width  int
	height int
}

func (p *trafficPage) setSize(w, h int) {
	p.width, p.height = w, h
}

func (p *trafficPage) update(snap driver.TrafficSnapshot) {
	p.snap = snap
	p.err = nil
}
