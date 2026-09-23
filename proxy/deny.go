package proxy

import "sync"

// DenySet records targets a user explicitly denied during this proxy's
// lifetime. It does not affect filtering, which blocks these targets anyway;
// it only lets every attached client know a decision was made so they stop
// prompting. Safe for concurrent use; the zero value is ready.
type DenySet struct {
	mu      sync.Mutex
	targets map[Target]bool
}

func (d *DenySet) Add(t Target) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.targets == nil {
		d.targets = make(map[Target]bool)
	}
	d.targets[t] = true
}

func (d *DenySet) Denied(t Target) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.targets[t]
}
