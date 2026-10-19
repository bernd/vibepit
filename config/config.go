package config

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/adrg/xdg"
	"github.com/bernd/vibepit/proxy"
	units "github.com/docker/go-units"
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
	AllowHTTP   []string `koanf:"allow-http"`
	AllowDNS    []string `koanf:"allow-dns"`
	BlockCIDR   []string `koanf:"block-cidr"`
	AllowCIDR   []string `koanf:"allow-cidr"`
	ExtraHosts  []string `koanf:"extra-hosts"`
	UpstreamDNS string   `koanf:"upstream-dns"`
	Memory      string   `koanf:"memory"`
}

type ProjectConfig struct {
	Presets        []string `koanf:"presets"`
	AllowHTTP      []string `koanf:"allow-http"`
	AllowDNS       []string `koanf:"allow-dns"`
	AllowHostPorts []int    `koanf:"allow-host-ports"`
	Memory         string   `koanf:"memory"`
}

type Config struct {
	Global  GlobalConfig
	Project ProjectConfig
}

type MergedConfig struct {
	AllowHTTP      []string `json:"allow-http"`
	AllowDNS       []string `json:"allow-dns"`
	BlockCIDR      []string `json:"block-cidr"`
	AllowCIDR      []string `json:"allow-cidr"`
	ExtraHosts     []string `json:"extra-hosts,omitempty"`
	UpstreamDNS    string   `json:"upstream-dns,omitempty"`
	AllowHostPorts []int    `json:"allow-host-ports"`
	ProxyIP        string   `json:"proxy-ip,omitempty"`
	HostGateway    string   `json:"host-gateway,omitempty"`
	ProxyPort      int      `json:"proxy-port,omitempty"`
	ControlAPIPort int      `json:"control-api-port,omitempty"`
	SSHForwardAddr string   `json:"ssh-forward-addr,omitempty"`
}

// RandomProxyPort returns a random port in the ephemeral range (49152-65535)
// that is not in the excluded set.
func RandomProxyPort(excluded []int) (int, error) {
	const lo, hi = 49152, 65535
	rangeSize := hi - lo + 1
	for range 100 {
		var b [2]byte
		if _, err := rand.Read(b[:]); err != nil {
			return 0, err
		}
		port := lo + int(binary.BigEndian.Uint16(b[:]))%rangeSize
		if !slices.Contains(excluded, port) {
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
func (c *Config) Merge(cliAllow []string, cliPresets []string) (MergedConfig, error) {
	allowHTTP := dedup(c.Global.AllowHTTP, c.Project.AllowHTTP, cliAllow)

	// Expand presets from both project config and CLI flags.
	reg := proxy.NewPresetRegistry()
	allowHTTP = dedup(allowHTTP, reg.Expand(append(c.Project.Presets, cliPresets...)))

	if err := proxy.ValidateHTTPEntries(allowHTTP); err != nil {
		return MergedConfig{}, fmt.Errorf("allow-http: %w", err)
	}

	allowDNS := dedup(c.Global.AllowDNS, c.Project.AllowDNS)

	if err := proxy.ValidateDNSEntries(allowDNS); err != nil {
		return MergedConfig{}, fmt.Errorf("allow-dns: %w", err)
	}

	return MergedConfig{
		AllowHTTP:      allowHTTP,
		AllowDNS:       allowDNS,
		BlockCIDR:      c.Global.BlockCIDR,
		AllowCIDR:      c.Global.AllowCIDR,
		ExtraHosts:     c.Global.ExtraHosts,
		UpstreamDNS:    c.Global.UpstreamDNS,
		AllowHostPorts: c.Project.AllowHostPorts,
	}, nil
}

// minMemoryLimit mirrors the smallest memory limit the Docker daemon accepts.
// Values below it are rejected at container creation, long after the network
// and proxy container have been set up, so reject them up front instead.
const minMemoryLimit = 6 << 20

// maxMemoryLimit bounds accepted values well below the int64 range.
// go-units converts the parsed float to int64 unchecked, so an overflowing
// value comes back negative on amd64 and saturated to MaxInt64 on arm64.
const maxMemoryLimit = 1 << 60

// Sources of the memory limit, as reported by MemoryLimit.
const (
	MemorySourceCLI     = "--memory flag"
	MemorySourceProject = "project config"
	MemorySourceGlobal  = "global config"
)

// MemoryLimit returns the sandbox memory limit in bytes, or 0 for no limit,
// along with the source the value came from ("" when unset). The CLI value
// takes precedence over project config, which takes precedence over global
// config. An explicit "0" at any level disables a limit set at a
// lower-precedence level.
func (c *Config) MemoryLimit(cliMemory string) (int64, string, error) {
	var memory, source string
	switch {
	case cliMemory != "":
		memory, source = cliMemory, MemorySourceCLI
	case c.Project.Memory != "":
		memory, source = c.Project.Memory, MemorySourceProject
	case c.Global.Memory != "":
		memory, source = c.Global.Memory, MemorySourceGlobal
	default:
		return 0, "", nil
	}
	limit, err := ParseMemoryLimit(memory)
	if err != nil {
		return 0, "", fmt.Errorf("%s: %w", source, err)
	}
	return limit, source, nil
}

// ParseMemoryLimit parses a docker-style memory size (e.g. "512m", "8g") into
// bytes. Empty and a zero size ("0", "0g", ...) both mean no limit and
// return 0.
func ParseMemoryLimit(memory string) (int64, error) {
	if memory == "" {
		return 0, nil
	}
	limit, err := units.RAMInBytes(memory)
	if err != nil {
		return 0, fmt.Errorf("invalid memory limit %q: %w", memory, err)
	}
	switch {
	case limit == 0 && isZeroSize(memory):
		return 0, nil
	case limit < 0 || limit > maxMemoryLimit:
		return 0, fmt.Errorf("invalid memory limit %q: must be at most %s",
			memory, units.BytesSize(maxMemoryLimit))
	case limit < minMemoryLimit:
		return 0, fmt.Errorf("invalid memory limit %q: must be at least %s (values without a unit suffix are bytes; use m or g)",
			memory, units.BytesSize(minMemoryLimit))
	}
	return limit, nil
}

// isZeroSize reports whether the number in a size string is zero. go-units
// truncates fractional byte counts, so "0.5" also parses to 0, but it is a
// typo for "0.5g" rather than a request to disable the limit.
func isZeroSize(size string) bool {
	rest := strings.TrimLeft(size, "0.")
	return rest == "" || rest[0] < '0' || rest[0] > '9'
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
