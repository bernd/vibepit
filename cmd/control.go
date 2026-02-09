package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/bernd/vibepit/config"
	"github.com/bernd/vibepit/proxy"
)

// ControlClient talks to a running proxy's control API over mTLS.
type ControlClient struct {
	http    *http.Client
	baseURL string
}

func NewControlClient(session *SessionInfo) (*ControlClient, error) {
	if session.ControlPort == "" {
		return nil, fmt.Errorf("missing control API port for session %q", session.SessionID)
	}
	tlsCfg, err := LoadSessionTLSConfig(session.SessionID)
	if err != nil {
		return nil, fmt.Errorf("load TLS credentials: %w", err)
	}
	return &ControlClient{
		http: &http.Client{
			Timeout:   5 * time.Second,
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
		},
		baseURL: fmt.Sprintf("https://127.0.0.1:%s", session.ControlPort),
	}, nil
}

func (c *ControlClient) Logs() ([]proxy.LogEntry, error) {
	var entries []proxy.LogEntry
	if err := c.get("/logs", &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func (c *ControlClient) LogsAfter(afterID uint64) ([]proxy.LogEntry, error) {
	var entries []proxy.LogEntry
	if err := c.get(fmt.Sprintf("/logs?after=%d", afterID), &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func (c *ControlClient) Stats() (map[string]proxy.DomainStats, error) {
	var stats map[string]proxy.DomainStats
	if err := c.get("/stats", &stats); err != nil {
		return nil, err
	}
	return stats, nil
}

func (c *ControlClient) Config() (*config.MergedConfig, error) {
	var cfg config.MergedConfig
	if err := c.get("/config", &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Allow adds domains to the proxy allowlist and returns the entries that were added.
func (c *ControlClient) Allow(entries []string) ([]string, error) {
	body, err := json.Marshal(map[string]any{"entries": entries})
	if err != nil {
		return nil, fmt.Errorf("marshal allow entries: %w", err)
	}
	resp, err := c.http.Post(c.baseURL+"/allow", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("POST /allow: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("POST /allow: %s", resp.Status)
	}

	var result struct {
		Added []string `json:"added"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode allow response: %w", err)
	}
	return result.Added, nil
}

func (c *ControlClient) get(path string, dest any) error {
	resp, err := c.http.Get(c.baseURL + path)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(dest)
}
