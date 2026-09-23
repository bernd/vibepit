package proxy

import "sync"

// DenySet records targets a user explicitly denied during this proxy's
// lifetime. It does not affect filtering, which blocks these targets anyway;
// it only lets every attached client know a decision was made so they stop
// prompting. Safe for concurrent use; the zero value is ready.
type DenySet struct {
	mu      sync.Mutex
	targets map[string]bool
}

func denyKey(source Source, target string) string {
	return string(source) + "/" + target
}

func (d *DenySet) Add(source Source, target string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.targets == nil {
		d.targets = make(map[string]bool)
	}
	d.targets[denyKey(source, target)] = true
}

func (d *DenySet) Denied(source Source, target string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.targets[denyKey(source, target)]
}
