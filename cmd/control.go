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

// Close releases idle connections held by the underlying HTTP transport.
func (c *ControlClient) Close() {
	c.http.CloseIdleConnections()
}

// Logs returns a recent tail of the log, for filling a screen on first load.
func (c *ControlClient) Logs() ([]proxy.LogEntry, error) {
	var entries []proxy.LogEntry
	if err := c.get("/logs", &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// LogsAfter returns every entry with an ID greater than afterID; 0 means all.
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
	q.Set("target", entry.Target().String())
	var res CheckResult
	if err := c.get("/check?"+q.Encode(), &res); err != nil {
		return CheckResult{}, err
	}
	return res, nil
}

// Deny records that the user refused the entry's target, so other clients
// stop prompting for it.
func (c *ControlClient) Deny(entry proxy.LogEntry) error {
	return c.post("/deny", map[string]string{
		"source": string(entry.Source),
		"target": entry.Target().String(),
	}, nil)
}

func (c *ControlClient) postAllow(path string, entries []string) ([]string, error) {
	var result struct {
		Added []string `json:"added"`
	}
	if err := c.post(path, map[string]any{"entries": entries}, &result); err != nil {
		return nil, err
	}
	return result.Added, nil
}

// post sends body as JSON and decodes the response into dest unless it is nil.
func (c *ControlClient) post(path string, body, dest any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal %s request: %w", path, err)
	}
	resp, err := c.http.Post(c.baseURL+path, "application/json", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("POST %s: %s", path, resp.Status)
	}
	if dest == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
		return fmt.Errorf("decode %s response: %w", path, err)
	}
	return nil
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
