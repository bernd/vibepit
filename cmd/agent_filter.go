package cmd

import "slices"

// agentFilter tracks known agents and cycles through them as a display filter.
type agentFilter struct {
	agents []string
	active string // "" means show all
}

func (f *agentFilter) cycle() {
	if len(f.agents) == 0 {
		return
	}
	if f.active == "" {
		f.active = f.agents[0]
		return
	}
	for i, a := range f.agents {
		if a == f.active {
			if i+1 < len(f.agents) {
				f.active = f.agents[i+1]
			} else {
				f.active = "" // back to all
			}
			return
		}
	}
	f.active = ""
}

func (f *agentFilter) track(agent string) {
	if slices.Contains(f.agents, agent) {
		return
	}
	f.agents = append(f.agents, agent)
}
