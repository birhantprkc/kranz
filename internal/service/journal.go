package service

import (
	"sync"
	"time"

	"github.com/kranz-org/kranz/internal/config"
)

// defaultJournalSize bounds recorded history the way log buffers bound output.
// A reader that falls further behind than this is told its window was
// truncated rather than shown a silently incomplete story.
const defaultJournalSize = 512

// Transition kinds. A reader dispatches on Kind before reading the fields that
// only one kind fills, so adding a kind later cannot change what an existing
// kind means.
const (
	TransitionServiceState = "service_state"
	TransitionServicePorts = "service_ports"
	TransitionActionState  = "action_state"
	TransitionConfigReload = "config_reload"
)

// Transition is one recorded change in the runtime. It exists because the
// difference between two status snapshots is not the same thing as what
// happened between them: a service that restarted and returned to running
// looks unchanged in a diff and obvious in a journal.
type Transition struct {
	Sequence uint64    `json:"sequence"`
	At       time.Time `json:"at"`
	Kind     string    `json:"kind"`
	// Service and Action name the subject. A service transition fills Service;
	// an action transition fills both Action and, for a service-owned action,
	// Service as its owner.
	Service string `json:"service,omitempty"`
	Action  string `json:"action,omitempty"`
	// From and To are the states the subject moved between. Service states are
	// config.ServiceStatus values; action states are ActionStatus values.
	From string `json:"from,omitempty"`
	To   string `json:"to"`
	// Run addresses the execution this transition belongs to: a service run
	// for a service, an action run for an action.
	Run      uint32             `json:"run,omitempty"`
	PID      int                `json:"pid,omitempty"`
	ExitCode *int               `json:"exit_code,omitempty"`
	Ports    []int              `json:"ports,omitempty"`
	Cause    *config.StateCause `json:"cause,omitempty"`
	// Generation is the configuration generation a reload moved to.
	Generation uint64 `json:"generation,omitempty"`
	// Summary is a short human sentence for the same fact. It is a convenience
	// for display, never the place a reader parses meaning out of.
	Summary string `json:"summary,omitempty"`
}

// Journal is the bounded, monotonically numbered record of runtime changes.
type Journal struct {
	mu      sync.RWMutex
	entries []Transition
	size    int
	write   int
	count   int
	next    uint64
}

// NewJournal creates a journal retaining at most size transitions.
func NewJournal(size int) *Journal {
	if size <= 0 {
		size = defaultJournalSize
	}
	return &Journal{entries: make([]Transition, size), size: size}
}

// Record stamps a transition with the next sequence number and stores it. A
// nil journal records nothing, so a Service constructed outside a Manager (as
// tests do) needs no special case at every call site.
func (j *Journal) Record(transition Transition) uint64 {
	if j == nil {
		return 0
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.next++
	transition.Sequence = j.next
	if transition.At.IsZero() {
		transition.At = time.Now()
	}
	j.entries[j.write] = transition
	j.write = (j.write + 1) % j.size
	if j.count < j.size {
		j.count++
	}
	return transition.Sequence
}

// Since returns transitions newer than sequence, oldest first, along with the
// journal's current bounds. truncated reports that entries after the requested
// sequence had already aged out, so the caller knows its story has a hole.
func (j *Journal) Since(sequence uint64, limit int) (transitions []Transition, oldest, latest uint64, truncated bool) {
	if j == nil {
		return nil, 0, 0, false
	}
	j.mu.RLock()
	defer j.mu.RUnlock()
	transitions = make([]Transition, 0, j.count)
	for index := range j.count {
		entry := j.entries[(j.write-j.count+index+j.size)%j.size]
		if index == 0 {
			oldest = entry.Sequence
		}
		latest = entry.Sequence
		if entry.Sequence > sequence {
			transitions = append(transitions, entry)
		}
	}
	if sequence > 0 && oldest > sequence+1 {
		truncated = true
	}
	if limit > 0 && len(transitions) > limit {
		transitions = transitions[len(transitions)-limit:]
		truncated = true
	}
	return transitions, oldest, latest, truncated
}

// Latest returns the newest recorded sequence, or zero when nothing has been
// recorded. A caller subscribing to future changes starts from it.
func (j *Journal) Latest() uint64 {
	if j == nil {
		return 0
	}
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.next
}
