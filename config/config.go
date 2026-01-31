package config

import (
	"os"
	"path/filepath"

	"github.com/bernd/vibepit/proxy"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

type GlobalConfig struct {
	Allow     []string `koanf:"allow"`
	DNSOnly   []string `koanf:"dns-only"`
	BlockCIDR []string `koanf:"block-cidr"`
	AllowHTTP bool     `koanf:"allow-http"`
}

type ProjectConfig struct {
	Presets   []string `koanf:"presets"`
	Allow     []string `koanf:"allow"`
	DNSOnly   []string `koanf:"dns-only"`
	AllowHTTP bool     `koanf:"allow-http"`
}

type Config struct {
	Global  GlobalConfig
	Project ProjectConfig
}

type MergedConfig struct {
	Allow     []string `json:"allow"`
	DNSOnly   []string `json:"dns-only"`
	BlockCIDR []string `json:"block-cidr"`
	AllowHTTP bool     `json:"allow-http"`
}

func Load(globalPath, projectPath string) (*Config, error) {
	cfg := &Config{}

	if err := loadFile(globalPath, &cfg.Global); err != nil {
		return nil, err
	}
	if err := loadFile(projectPath, &cfg.Project); err != nil {
		return nil, err
	}

	return cfg, nil
}

// loadFile parses a YAML file into target, silently skipping missing files
// so callers don't need to check existence first.
func loadFile(path string, target any) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}

	k := koanf.New(".")
	if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
		return err
	}
	return k.Unmarshal("", target)
}

// Merge combines global config, project config, CLI flags, and expanded presets
// into a single flat config. Duplicates are removed while preserving order.
func (c *Config) Merge(cliAllow []string, cliPresets []string) MergedConfig {
	seen := make(map[string]bool)
	var allow []string

	addUnique := func(entries []string) {
		for _, e := range entries {
			if !seen[e] {
				seen[e] = true
				allow = append(allow, e)
			}
		}
	}

	addUnique(c.Global.Allow)
	addUnique(c.Project.Allow)
	addUnique(cliAllow)

	// Expand presets from both project config and CLI flags.
	allPresets := make([]string, 0, len(c.Project.Presets)+len(cliPresets))
	allPresets = append(allPresets, c.Project.Presets...)
	allPresets = append(allPresets, cliPresets...)

	reg := proxy.NewPresetRegistry()
	addUnique(reg.Expand(allPresets))

	var dnsOnly []string
	dnsSeen := make(map[string]bool)
	for _, e := range c.Global.DNSOnly {
		if !dnsSeen[e] {
			dnsSeen[e] = true
			dnsOnly = append(dnsOnly, e)
		}
	}
	for _, e := range c.Project.DNSOnly {
		if !dnsSeen[e] {
			dnsSeen[e] = true
			dnsOnly = append(dnsOnly, e)
		}
	}

	return MergedConfig{
		Allow:     allow,
		DNSOnly:   dnsOnly,
		BlockCIDR: c.Global.BlockCIDR,
		AllowHTTP: c.Global.AllowHTTP || c.Project.AllowHTTP,
	}
}

func DefaultGlobalPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "vibepit", "config.yaml")
}

func DefaultProjectPath(projectRoot string) string {
	return filepath.Join(projectRoot, ".vibepit", "network.yaml")
}
