package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/bernd/vibepit/proxy"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

const RuntimeDirName = "vibepit"

type GlobalConfig struct {
	Allow     []string `koanf:"allow"`
	DNSOnly   []string `koanf:"dns-only"`
	BlockCIDR []string `koanf:"block-cidr"`
	AllowHTTP bool     `koanf:"allow-http"`
}

type ProjectConfig struct {
	Presets        []string `koanf:"presets"`
	Allow          []string `koanf:"allow"`
	DNSOnly        []string `koanf:"dns-only"`
	AllowHTTP      bool     `koanf:"allow-http"`
	AllowHostPorts []int    `koanf:"allow-host-ports"`
}

type Config struct {
	Global  GlobalConfig
	Project ProjectConfig
}

type MergedConfig struct {
	Allow          []string `json:"allow"`
	DNSOnly        []string `json:"dns-only"`
	BlockCIDR      []string `json:"block-cidr"`
	AllowHTTP      bool     `json:"allow-http"`
	AllowHostPorts []int    `json:"allow-host-ports"`
	ProxyIP        string   `json:"proxy-ip,omitempty"`
	HostGateway    string   `json:"host-gateway,omitempty"`
}

// reservedProxyPorts are ports used internally by the proxy and cannot be
// forwarded to the host.
var reservedProxyPorts = map[int]string{
	53:   "DNS server",
	2222: "SSH server",
	3128: "HTTP proxy",
	3129: "control API",
}

// ValidateHostPorts checks that none of the configured host ports conflict
// with ports reserved by the proxy.
func (m *MergedConfig) ValidateHostPorts() error {
	for _, port := range m.AllowHostPorts {
		if service, ok := reservedProxyPorts[port]; ok {
			return fmt.Errorf("allow-host-ports: port %d is reserved for %s", port, service)
		}
	}
	return nil
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
	allow := dedup(c.Global.Allow, c.Project.Allow, cliAllow)

	// Expand presets from both project config and CLI flags.
	allPresets := append(c.Project.Presets, cliPresets...)
	reg := proxy.NewPresetRegistry()
	allow = dedup(allow, reg.Expand(allPresets))

	dnsOnly := dedup(c.Global.DNSOnly, c.Project.DNSOnly)

	return MergedConfig{
		Allow:          allow,
		DNSOnly:        dnsOnly,
		BlockCIDR:      c.Global.BlockCIDR,
		AllowHTTP:      c.Global.AllowHTTP || c.Project.AllowHTTP,
		AllowHostPorts: c.Project.AllowHostPorts,
	}
}

// dedup merges multiple string slices, removing duplicates while preserving order.
func dedup(slices ...[]string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, s := range slices {
		for _, e := range s {
			if !seen[e] {
				seen[e] = true
				result = append(result, e)
			}
		}
	}
	return result
}

// FindProjectRoot returns the Git repository root for the given path, or the
// path itself if it is not inside a Git repository.
func FindProjectRoot(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if out, err := exec.Command("git", "-C", abs, "rev-parse", "--show-toplevel").Output(); err == nil {
		if root := strings.TrimSpace(string(out)); root != "" {
			return root, nil
		}
	}
	return abs, nil
}

func DefaultGlobalPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "vibepit", "config.yaml")
}

func DefaultProjectPath(projectRoot string) string {
	return filepath.Join(projectRoot, ".vibepit", "network.yaml")
}
