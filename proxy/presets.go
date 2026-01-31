package proxy

import (
	_ "embed"

	"gopkg.in/yaml.v3"
)

//go:embed presets.yaml
var presetsYAML []byte

// Preset defines a network allowlist preset with optional auto-detection
// matchers and the ability to include other presets.
type Preset struct {
	Name        string   `yaml:"name"`
	Group       string   `yaml:"group"`
	Description string   `yaml:"description"`
	Domains     []string `yaml:"domains"`
	Matchers    []string `yaml:"matchers"` // file glob patterns for project auto-detection
	Includes    []string `yaml:"includes"` // other preset names (meta-presets)
}

// PresetRegistry holds all built-in presets in definition order.
type PresetRegistry struct {
	presets []Preset
	index   map[string]int
}

// NewPresetRegistry returns the built-in preset registry.
func NewPresetRegistry() *PresetRegistry {
	var presets []Preset
	if err := yaml.Unmarshal(presetsYAML, &presets); err != nil {
		panic("presets.yaml: " + err.Error())
	}

	index := make(map[string]int, len(presets))
	for i, p := range presets {
		index[p.Name] = i
	}

	return &PresetRegistry{presets: presets, index: index}
}

// Get returns a preset by name.
func (r *PresetRegistry) Get(name string) (Preset, bool) {
	i, ok := r.index[name]
	if !ok {
		return Preset{}, false
	}
	return r.presets[i], true
}

// All returns all presets in definition order.
func (r *PresetRegistry) All() []Preset {
	return r.presets
}

// Expand resolves a list of preset names into a deduplicated flat list of
// domains, recursively expanding Includes with cycle detection.
func (r *PresetRegistry) Expand(names []string) []string {
	seen := make(map[string]bool)
	var domains []string
	visited := make(map[string]bool)

	var expand func(name string)
	expand = func(name string) {
		if visited[name] {
			return
		}
		visited[name] = true
		p, ok := r.Get(name)
		if !ok {
			return
		}
		for _, inc := range p.Includes {
			expand(inc)
		}
		for _, d := range p.Domains {
			if !seen[d] {
				seen[d] = true
				domains = append(domains, d)
			}
		}
	}

	for _, name := range names {
		expand(name)
	}
	return domains
}
