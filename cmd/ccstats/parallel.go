package main

import (
	"sort"
	"time"
)

type parallelEvent struct {
	t       time.Time
	session string
	delta   int
}

// ParallelIndex answers "how many sessions were active at t?" using a sparse
// event list of request activity windows.
type ParallelIndex struct {
	events []parallelEvent
	built  bool
}

// Add records a single request window for the given session.
func (p *ParallelIndex) Add(sessionID string, at time.Time, window time.Duration) {
	if sessionID == "" || at.IsZero() || window <= 0 {
		return
	}
	p.events = append(p.events,
		parallelEvent{t: at, session: sessionID, delta: 1},
		parallelEvent{t: at.Add(window), session: sessionID, delta: -1},
	)
	p.built = false
}

// Build sorts and normalizes the sparse event list.
func (p *ParallelIndex) Build() {
	if p.built {
		return
	}
	sort.SliceStable(p.events, func(i, j int) bool {
		if p.events[i].t.Equal(p.events[j].t) {
			if p.events[i].delta == p.events[j].delta {
				return p.events[i].session < p.events[j].session
			}
			return p.events[i].delta < p.events[j].delta
		}
		return p.events[i].t.Before(p.events[j].t)
	})
	p.built = true
}

// ActiveAt reports how many distinct sessions have active windows containing t.
func (p *ParallelIndex) ActiveAt(t time.Time) int {
	if len(p.events) == 0 {
		return 0
	}
	p.Build()

	active := make(map[string]int)
	for _, ev := range p.events {
		if ev.t.After(t) {
			break
		}
		active[ev.session] += ev.delta
		if active[ev.session] <= 0 {
			delete(active, ev.session)
		}
	}
	return len(active)
}
