package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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
	credDir := session.CredDir
	if credDir == "" {
		credDir = sessionDir(session.SessionID)
	}
	tlsCfg, err := loadTLSConfigFromDir(credDir)
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

// Close releases idle connections held by the underlying HTTP transport.
func (c *ControlClient) Close() {
	c.http.CloseIdleConnections()
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

// LogsSince returns every entry with an ID greater than sinceID. Use it for
// incremental polling; LogsAfter(0) only returns a recent tail.
func (c *ControlClient) LogsSince(sinceID uint64) ([]proxy.LogEntry, error) {
	var entries []proxy.LogEntry
	if err := c.get(fmt.Sprintf("/logs?since=%d", sinceID), &entries); err != nil {
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

// AllowHTTP adds domains to the proxy HTTP allowlist and returns the entries that were added.
func (c *ControlClient) AllowHTTP(entries []string) ([]string, error) {
	return c.postAllow("/allow-http", entries)
}

// AllowDNS adds domains to the proxy DNS allowlist and returns the entries that were added.
func (c *ControlClient) AllowDNS(entries []string) ([]string, error) {
	return c.postAllow("/allow-dns", entries)
}

// CheckResult is the proxy's view of a blocked target: allowed by the live
// allowlist, or denied by a user. Both mean nobody needs to be asked.
type CheckResult struct {
	Allowed bool `json:"allowed"`
	Denied  bool `json:"denied"`
}

// Decided reports whether a user decision exists for the target.
func (r CheckResult) Decided() bool { return r.Allowed || r.Denied }

// Check asks the proxy whether the entry's target is allowed or denied.
func (c *ControlClient) Check(entry proxy.LogEntry) (CheckResult, error) {
	q := url.Values{}
	q.Set("source", string(entry.Source))
	q.Set("target", allowValueForEntry(entry))
	var res CheckResult
	if err := c.get("/check?"+q.Encode(), &res); err != nil {
		return CheckResult{}, err
	}
	return res, nil
}

// Deny records that the user refused the entry's target, so other clients
// stop prompting for it.
func (c *ControlClient) Deny(entry proxy.LogEntry) error {
	body, err := json.Marshal(map[string]string{
		"source": string(entry.Source),
		"target": allowValueForEntry(entry),
	})
	if err != nil {
		return fmt.Errorf("marshal deny: %w", err)
	}
	resp, err := c.http.Post(c.baseURL+"/deny", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("POST /deny: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("POST /deny: %s", resp.Status)
	}
	return nil
}

func (c *ControlClient) postAllow(path string, entries []string) ([]string, error) {
	body, err := json.Marshal(map[string]any{"entries": entries})
	if err != nil {
		return nil, fmt.Errorf("marshal allow entries: %w", err)
	}
	resp, err := c.http.Post(c.baseURL+path, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("POST %s: %s", path, resp.Status)
	}

	var result struct {
		Added []string `json:"added"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode %s response: %w", path, err)
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
