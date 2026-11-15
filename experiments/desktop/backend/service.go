package backend

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/adrg/xdg"
	"github.com/bernd/vibepit/cmd"
	"github.com/bernd/vibepit/config"
	ctr "github.com/bernd/vibepit/container"
	"github.com/bernd/vibepit/proxy"
	"golang.org/x/crypto/ssh"
)

type Event struct {
	Workspace  string `json:"workspace,omitempty"`
	Connection string `json:"connection"`
	Kind       string `json:"kind"`
	Data       any    `json:"data"`
}

type Sandbox struct {
	ID      string `json:"id"`
	Project string `json:"project"`
	Demo    bool   `json:"demo"`
}

type Service struct {
	mu     sync.Mutex
	demo   bool
	emit   func(Event)
	conn   *connection
	logDir string
}

type connection struct {
	id       string
	ctx      context.Context
	cancel   context.CancelFunc
	active   atomic.Bool
	ssh      *ssh.Client
	control  *cmd.ControlClient
	terminal *ssh.Session
	input    io.WriteCloser
	agent    *agent
	emit     func(Event)
	mu       sync.Mutex
	pending  map[uint64]proxy.LogEntry
	watching bool
	sandbox  Sandbox
}

func New(demo bool, emit func(Event), logDir string) *Service {
	return &Service{demo: demo, emit: emit, logDir: logDir}
}

func (s *Service) Sessions() ([]Sandbox, error) {
	if s.demo {
		return []Sandbox{{ID: "demo", Project: "/workspace/vibepit", Demo: true}}, nil
	}
	client, err := ctr.NewClient()
	if err != nil {
		return nil, err
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	list, err := client.ListProxySessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("discover sandboxes: %w", err)
	}
	result := make([]Sandbox, 0, len(list))
	for _, item := range list {
		result = append(result, Sandbox{ID: item.SessionID, Project: item.ProjectDir})
	}
	return result, nil
}

func (s *Service) Connect(id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		return "", fmt.Errorf("disconnect the current sandbox first")
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &connection{id: rand.Text(), ctx: ctx, cancel: cancel, emit: s.emit, pending: make(map[uint64]proxy.LogEntry)}
	if s.demo {
		if id != "demo" {
			cancel()
			return "", fmt.Errorf("unknown demo sandbox")
		}
		c.sandbox = Sandbox{ID: id, Project: "/workspace/vibepit", Demo: true}
	} else {
		if err := c.open(id); err != nil {
			cancel()
			return "", err
		}
	}
	c.active.Store(true)
	s.conn = c
	return c.id, nil
}

func (c *connection) open(id string) error {
	client, err := ctr.NewClient()
	if err != nil {
		return err
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()
	list, err := client.ListProxySessions(ctx)
	if err != nil {
		return err
	}
	var found *ctr.ProxySession
	for _, item := range list {
		if item.SessionID == id {
			found = &item
			break
		}
	}
	if found == nil {
		return fmt.Errorf("sandbox disappeared; refresh the list")
	}
	c.sandbox = Sandbox{ID: id, Project: found.ProjectDir}
	port, err := client.FindPublishedPort(ctx, found.ContainerID, ctr.SSHContainerPort)
	if err != nil {
		return fmt.Errorf("SSH unavailable; start the sandbox with vibepit up: %w", err)
	}
	dir := filepath.Join(xdg.StateHome, config.RuntimeDirName, "sessions", id)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		dir = filepath.Join(xdg.RuntimeDir, config.RuntimeDirName, id)
	}
	key, err := os.ReadFile(filepath.Join(dir, cmd.SSHClientPrivFile))
	if err != nil {
		return err
	}
	host, err := os.ReadFile(filepath.Join(dir, cmd.SSHHostPubFile))
	if err != nil {
		return err
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return err
	}
	hostKey, _, _, _, err := ssh.ParseAuthorizedKey(host)
	if err != nil {
		return err
	}
	c.control, err = cmd.NewControlClient(&cmd.SessionInfo{SessionID: id, ProjectDir: found.ProjectDir, ControlPort: found.ControlPort})
	if err != nil {
		return err
	}
	c.ssh, err = ssh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port), &ssh.ClientConfig{
		User: "code", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.FixedHostKey(hostKey), Timeout: 10 * time.Second,
	})
	if err != nil {
		c.control.Close()
		return err
	}
	return nil
}

func (c *connection) publish(kind string, data any) {
	if c.active.Load() && c.emit != nil {
		c.emit(Event{Connection: c.id, Kind: kind, Data: data})
	}
}

func (s *Service) Disconnect() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.conn; c != nil {
		c.active.Store(false)
		c.cancel()
		if c.agent != nil {
			c.agent.close()
		}
		if c.ssh != nil {
			c.ssh.Close()
		}
		if c.control != nil {
			c.control.Close()
		}
		s.conn = nil
	}
}

const demoShellPrompt = "\x1b]7;file://vibepit/home/code/project\x07\x1b]133;A\x07code@vibepit:~/project$ \x1b]133;B\x07"

func (s *Service) OpenTerminal(token string, cols, rows int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.conn
	if c == nil || c.id != token {
		return fmt.Errorf("connect to a sandbox first")
	}
	if c.terminal != nil {
		return fmt.Errorf("terminal is already open")
	}
	if cols < 2 || rows < 1 || cols > 1000 || rows > 1000 {
		return fmt.Errorf("invalid terminal dimensions")
	}
	if s.demo {
		c.publish("terminal", base64.StdEncoding.EncodeToString([]byte("\x1b[2J\x1b[H\x1b[38;5;150mVibepit Lab · simulated terminal\x1b[0m\r\n\r\nThis preview does not execute commands.\r\nOpen Claude to try a streamed reply and permission requests.\r\n\r\n"+demoShellPrompt)))
		return nil
	}
	t, err := c.ssh.NewSession()
	if err != nil {
		return err
	}
	if err := t.Setenv("VIBEPIT_SHELL_INTEGRATION", "1"); err != nil {
		t.Close()
		return fmt.Errorf("enable shell integration: %w", err)
	}
	if err := t.RequestPty("xterm-256color", rows, cols, ssh.TerminalModes{ssh.ECHO: 1}); err != nil {
		t.Close()
		return err
	}
	input, err := t.StdinPipe()
	if err != nil {
		t.Close()
		return err
	}
	t.Stdout = terminalWriter{c}
	t.Stderr = terminalWriter{c}
	if err := t.Shell(); err != nil {
		t.Close()
		return err
	}
	c.terminal, c.input = t, input
	go func() {
		err := t.Wait()
		message := "Shell exited. Disconnect and reconnect to open another terminal."
		if err != nil {
			message = "Terminal disconnected: " + err.Error()
		}
		c.publish("terminal-exit", message)
	}()
	return nil
}

type terminalWriter struct{ c *connection }

func (w terminalWriter) Write(p []byte) (int, error) {
	// Preserve arbitrary bytes, including UTF-8 split across SSH reads.
	w.c.publish("terminal", base64.StdEncoding.EncodeToString(p))
	return len(p), nil
}

// WriteTerminalBytes preserves binary input such as legacy mouse coordinates.
func (s *Service) WriteTerminalBytes(token, encoded string) error {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return fmt.Errorf("invalid terminal input: %w", err)
	}
	return s.WriteTerminal(token, string(data))
}

func (s *Service) WriteTerminal(token, data string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.conn
	if c == nil || c.id != token {
		return fmt.Errorf("terminal is disconnected")
	}
	if s.demo {
		if data == "\r" {
			data = "\x1b]133;C\x07\r\nPreview only — no command was run.\r\n\x1b]133;D;0\x07" + demoShellPrompt
		}
		c.publish("terminal", base64.StdEncoding.EncodeToString([]byte(data)))
		return nil
	}
	if c.input == nil {
		return fmt.Errorf("terminal is not open")
	}
	_, err := io.WriteString(c.input, data)
	return err
}

func (s *Service) ResizeTerminal(token string, cols, rows int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cols < 2 || rows < 1 || cols > 1000 || rows > 1000 {
		return fmt.Errorf("invalid terminal dimensions")
	}
	if s.conn == nil || s.conn.id != token {
		return nil
	}
	if s.conn.terminal == nil {
		return nil
	}
	return s.conn.terminal.WindowChange(rows, cols)
}

func (s *Service) WatchNetwork(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.conn
	if c == nil || c.id != token {
		return fmt.Errorf("connect to a sandbox first")
	}
	if c.watching {
		return nil
	}
	c.watching = true
	if s.demo {
		c.publish("network-status", "Watching")
		return nil
	}
	go c.watchNetwork()
	return nil
}

func (c *connection) watchNetwork() {
	for c.ctx.Err() == nil {
		stream, err := c.control.EventsTail(c.ctx, 0)
		if err == nil {
			c.mu.Lock()
			c.pending = make(map[uint64]proxy.LogEntry)
			c.mu.Unlock()
			c.publish("network-reset", nil)
			c.publish("network-status", "Watching")
			for {
				event, readErr := stream.Next()
				if readErr != nil {
					err = readErr
					break
				}
				c.networkEvent(event)
			}
			stream.Close()
		}
		c.publish("network-status", "Reconnecting: "+err.Error())
		select {
		case <-c.ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (c *connection) networkEvent(e proxy.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e.Type == proxy.EventHoldEnd {
		delete(c.pending, e.HoldEnd.ID)
		c.publish("network-end", e.HoldEnd)
		return
	}
	if e.Type != proxy.EventLog && e.Type != proxy.EventHoldOpen {
		return
	}
	entry := e.Log
	if entry.Action == proxy.ActionHold && entry.Outcome == "" && entry.Allowable() {
		for id, old := range c.pending {
			if old.HostKey() == entry.HostKey() || time.Now().After(old.Deadline) {
				delete(c.pending, id)
			}
		}
		c.pending[entry.ID] = entry
		c.publish("network-hold", entry)
	}
	c.publish("network-log", entry)
}

func (s *Service) DecideNetwork(token string, id uint64, allow bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.conn
	if c == nil || c.id != token {
		return fmt.Errorf("sandbox is disconnected")
	}
	c.mu.Lock()
	entry, ok := c.pending[id]
	c.mu.Unlock()
	if !ok || !time.Now().Before(entry.Deadline) {
		return fmt.Errorf("request expired; wait for a new request")
	}
	if s.demo {
		outcome := proxy.HoldRejected
		if allow {
			outcome = proxy.HoldAllowed
		}
		c.networkEvent(proxy.Event{Type: proxy.EventHoldEnd, HoldEnd: proxy.HoldEnd{ID: id, HostKey: entry.HostKey(), Outcome: outcome}})
		return nil
	}
	if allow {
		_, err := c.control.AllowHTTP([]string{entry.Target().String()})
		return err
	}
	return c.control.Deny(entry)
}
