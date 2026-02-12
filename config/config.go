package config

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/adrg/xdg"
	"github.com/bernd/vibepit/proxy"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

const (
	RuntimeDirName       = "vibepit"
	CacheDirName         = "vibepit"
	ConfigDirName        = "vibepit"
	ProjectConfigDirName = ".vibepit"
)

type GlobalConfig struct {
	AllowHTTP []string `koanf:"allow-http"`
	AllowDNS  []string `koanf:"allow-dns"`
	BlockCIDR []string `koanf:"block-cidr"`
}

type ProjectConfig struct {
	Presets        []string `koanf:"presets"`
	AllowHTTP      []string `koanf:"allow-http"`
	AllowDNS       []string `koanf:"allow-dns"`
	AllowHostPorts []int    `koanf:"allow-host-ports"`
}

type Config struct {
	Global  GlobalConfig
	Project ProjectConfig
}

type MergedConfig struct {
	AllowHTTP      []string `json:"allow-http"`
	AllowDNS       []string `json:"allow-dns"`
	BlockCIDR      []string `json:"block-cidr"`
	AllowHostPorts []int    `json:"allow-host-ports"`
	ProxyIP        string   `json:"proxy-ip,omitempty"`
	HostGateway    string   `json:"host-gateway,omitempty"`
	ProxyPort      int      `json:"proxy-port,omitempty"`
	ControlAPIPort int      `json:"control-api-port,omitempty"`
}

// RandomProxyPort returns a random port in the ephemeral range (49152-65535)
// that is not in the excluded set.
func RandomProxyPort(excluded map[int]bool) (int, error) {
	const lo, hi = 49152, 65535
	rangeSize := hi - lo + 1
	for range 100 {
		var b [2]byte
		if _, err := rand.Read(b[:]); err != nil {
			return 0, err
		}
		port := lo + int(binary.BigEndian.Uint16(b[:]))%rangeSize
		if !excluded[port] {
			return port, nil
		}
	}
	return 0, fmt.Errorf("failed to find available port after 100 attempts")
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
	allowHTTP := dedup(c.Global.AllowHTTP, c.Project.AllowHTTP, cliAllow)

	// Expand presets from both project config and CLI flags.
	reg := proxy.NewPresetRegistry()
	allowHTTP = dedup(allowHTTP, reg.Expand(append(c.Project.Presets, cliPresets...)))

	allowDNS := dedup(c.Global.AllowDNS, c.Project.AllowDNS)

	return MergedConfig{
		AllowHTTP:      allowHTTP,
		AllowDNS:       allowDNS,
		BlockCIDR:      c.Global.BlockCIDR,
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
	configHome := xdg.ConfigHome
	if configHome == "" {
		home, _ := os.UserHomeDir()
		configHome = filepath.Join(home, ".cache")
	}
	return filepath.Join(configHome, ConfigDirName, "config.yaml")
}

func DefaultProjectPath(projectRoot string) string {
	return filepath.Join(projectRoot, ProjectConfigDirName, "network.yaml")
}
