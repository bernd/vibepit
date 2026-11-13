package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/bernd/vibepit/config"
	"github.com/bernd/vibepit/proxy"
)

// ErrUnsupportedEndpoint means the proxy predates the endpoint, e.g. a session
// started before the monitor binary was upgraded.
var ErrUnsupportedEndpoint = errors.New("endpoint not supported by proxy")

// ControlClient talks to a running proxy's control API over mTLS.
type ControlClient struct {
	http *http.Client
	// stream has no overall timeout, which would end an event stream; its
	// requests live as long as their context.
	stream  *http.Client
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
	transport := &http.Transport{TLSClientConfig: tlsCfg}
	return &ControlClient{
		http:    &http.Client{Timeout: 5 * time.Second, Transport: transport},
		stream:  &http.Client{Transport: transport},
		baseURL: fmt.Sprintf("https://127.0.0.1:%s", session.ControlPort),
	}, nil
}

// Close releases idle connections held by the underlying HTTP transport.
func (c *ControlClient) Close() {
	c.http.CloseIdleConnections()
}

// HostsSnapshot is a /hosts result. LastID is the last log entry the stats
// include; Exact is false for a proxy that predates reporting it.
type HostsSnapshot struct {
	Hosts  []proxy.HostStats
	LastID uint64
	Exact  bool
}

func (c *ControlClient) Hosts() (HostsSnapshot, error) {
	var snap HostsSnapshot
	header, err := c.getHeader("/hosts", &snap.Hosts)
	if err != nil {
		return HostsSnapshot{}, err
	}
	if v := header.Get(proxy.HostsLastIDHeader); v != "" {
		id, err := strconv.ParseUint(v, 10, 64)
		snap.LastID, snap.Exact = id, err == nil
	}
	return snap, nil
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
	return c.postEntries("/allow-http", entries)
}

// AllowDNS adds domains to the proxy DNS allowlist and returns the entries that were added.
func (c *ControlClient) AllowDNS(entries []string) ([]string, error) {
	return c.postEntries("/allow-dns", entries)
}

// DenyHTTP adds enforced HTTP deny rules to the proxy and returns the entries that were added.
func (c *ControlClient) DenyHTTP(entries []string) ([]string, error) {
	return c.postEntries("/deny-http", entries)
}

// DenyDNS adds enforced DNS deny rules to the proxy and returns the entries that were added.
func (c *ControlClient) DenyDNS(entries []string) ([]string, error) {
	return c.postEntries("/deny-dns", entries)
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
// stop prompting for it. An older proxy without /deny can't record it; the
// refusal then stays with this client, which doesn't ask twice anyway.
func (c *ControlClient) Deny(entry proxy.LogEntry) error {
	return c.DenyTarget(entry.Target())
}

// DenyTarget is Deny for a target without a log entry at hand.
func (c *ControlClient) DenyTarget(t proxy.Target) error {
	err := c.post("/deny", map[string]string{
		"source": string(t.Source),
		"target": t.String(),
	}, nil)
	var se *statusError
	if errors.As(err, &se) && se.code == http.StatusNotFound {
		return nil
	}
	return err
}

// EventsTail opens the event stream with the newest n log entries as
// history. The stream ends when ctx does.
func (c *ControlClient) EventsTail(ctx context.Context, n int) (*EventStream, error) {
	return c.events(ctx, fmt.Sprintf("tail=%d", n))
}

// EventsAfter opens the event stream with every log entry above after as
// history, for resuming where an earlier stream ended.
func (c *ControlClient) EventsAfter(ctx context.Context, after uint64) (*EventStream, error) {
	return c.events(ctx, fmt.Sprintf("after=%d", after))
}

func (c *ControlClient) events(ctx context.Context, query string) (*EventStream, error) {
	path := "/events?" + query
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.stream.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	if err := getStatus(path, resp); err != nil {
		resp.Body.Close()
		return nil, err
	}
	body := &streamBody{ReadCloser: resp.Body}
	return &EventStream{body: body, r: bufio.NewReader(body)}, nil
}

// EventStream reads events from the proxy's /events stream.
type EventStream struct {
	body *streamBody
	r    *bufio.Reader
}

// Next returns the next event. It returns io.EOF when the proxy ends the
// stream, and events of types this client doesn't know with only Type set.
func (s *EventStream) Next() (proxy.Event, error) {
	return proxy.ReadEvent(s.r)
}

// Close ends the stream. It may be called while Next is blocked.
func (s *EventStream) Close() error {
	return s.body.Close()
}

// onRead registers f to run after every read that returned data, keepalive
// comments included, so a caller can tell a quiet stream from a dead one.
func (s *EventStream) onRead(f func()) {
	s.body.onRead = f
}

type streamBody struct {
	io.ReadCloser
	onRead func()
}

func (b *streamBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 && b.onRead != nil {
		b.onRead()
	}
	return n, err
}

// statusError is a control API response other than 200 OK.
type statusError struct {
	method, path string
	code         int
	status       string
	msg          string // the API's {"error": ...} message, if any
}

func (e *statusError) Error() string {
	if e.msg != "" {
		return fmt.Sprintf("%s %s: %s: %s", e.method, e.path, e.status, e.msg)
	}
	return fmt.Sprintf("%s %s: %s", e.method, e.path, e.status)
}

// apiErrorMessage reads the control API's JSON error body, e.g. why an allow
// was refused. Anything else yields "".
func apiErrorMessage(body io.Reader) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.NewDecoder(io.LimitReader(body, 4096)).Decode(&e) != nil {
		return ""
	}
	return e.Error
}

func (c *ControlClient) postEntries(path string, entries []string) ([]string, error) {
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
		return &statusError{method: "POST", path: path, code: resp.StatusCode, status: resp.Status, msg: apiErrorMessage(resp.Body)}
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
	_, err := c.getHeader(path, dest)
	return err
}

// getHeader is get for callers that also read the response header.
func (c *ControlClient) getHeader(path string, dest any) (http.Header, error) {
	resp, err := c.http.Get(c.baseURL + path)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	if err := getStatus(path, resp); err != nil {
		return nil, err
	}
	return resp.Header, json.NewDecoder(resp.Body).Decode(dest)
}

// getStatus turns a GET response other than 200 OK into an error. A 404
// means the proxy predates the endpoint.
func getStatus(path string, resp *http.Response) error {
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		return fmt.Errorf("GET %s: %s: %w", path, resp.Status, ErrUnsupportedEndpoint)
	default:
		return &statusError{method: "GET", path: path, code: resp.StatusCode, status: resp.Status}
	}
}
