package backend

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/bernd/vibepit/proxy"
)

// DesktopHooks are installed before app.Run. Preview builds leave native hooks unset.
type DesktopHooks struct {
	Native              bool
	Open                func(string) error
	Hide                func(string) error
	EnableNotifications func() (string, error)
	ReadClipboard       func() (string, error)
	WriteClipboard      func(string) error
}
type WindowInfo struct {
	NotifyWhenFocused bool   `json:"notifyWhenFocused"`
	ID                string `json:"id"`
	URL               string `json:"url"`
	Native            bool   `json:"native"`
	Title             string `json:"title"`
	Project           string `json:"project"`
	Connection        string `json:"connection"`
	AgentStatus       string `json:"agentStatus"`
	Connected         bool   `json:"connected"`
	Pending           int    `json:"pending"`
}
type Notice struct {
	ID         string
	WindowID   string
	Connection string
	Kind       string
	Title      string
	Body       string
	Deadline   time.Time
}
type workspace struct {
	lifecycle    sync.Mutex
	service      *Service
	info         WindowInfo
	number       int
	notices      map[string]Notice
	notified     map[string]bool
	results      map[string]bool
	turnFinished bool
	turn         uint64
}

// Workspaces owns connections independently of native window visibility.
// Event summaries are cached here: native callbacks never need a Service lock.
type Workspaces struct {
	mu       sync.Mutex
	demo     bool
	logDir   string
	emit     func(Event)
	windows  map[string]*workspace
	sequence int
	closed   bool
	Hooks    DesktopHooks
	OnChange func()
	OnNotice func(Notice)
}

func NewWorkspaces(demo bool, emit func(Event), logDir string) *Workspaces {
	return &Workspaces{demo: demo, emit: emit, logDir: logDir, windows: make(map[string]*workspace)}
}
func (r *Workspaces) lookup(id string) (*workspace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, fmt.Errorf("application is shutting down")
	}
	w, ok := r.windows[id]
	if !ok {
		return nil, fmt.Errorf("unknown workspace; open a new window")
	}
	return w, nil
}
func (r *Workspaces) session(id, token string) (*Service, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w := r.windows[id]
	if r.closed || w == nil || token == "" || w.info.Connection != token {
		return nil, fmt.Errorf("connection does not belong to this workspace")
	}
	return w.service, nil
}
func (r *Workspaces) NewWindow() (WindowInfo, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return WindowInfo{}, fmt.Errorf("application is shutting down")
	}
	r.sequence++
	id := rand.Text()
	info := WindowInfo{ID: id, URL: "/?workspace=" + id, Native: r.Hooks.Native, Title: fmt.Sprintf("Workspace %d", r.sequence), AgentStatus: "Not started"}
	w := &workspace{info: info, number: r.sequence, notices: make(map[string]Notice), notified: make(map[string]bool), results: make(map[string]bool)}
	w.service = New(r.demo, func(e Event) { r.receive(id, e) }, r.logDir)
	r.windows[id] = w
	r.mu.Unlock()
	if r.Hooks.Open != nil {
		if err := r.Hooks.Open(id); err != nil {
			r.mu.Lock()
			delete(r.windows, id)
			r.mu.Unlock()
			return WindowInfo{}, err
		}
	}
	r.changed()
	return info, nil
}
func (r *Workspaces) GetWindow(id string) (WindowInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.windows[id] == nil {
		return WindowInfo{}, fmt.Errorf("workspace is unavailable; open a new window")
	}
	return r.infoLocked(r.windows[id], time.Now()), nil
}
func (r *Workspaces) infoLocked(w *workspace, now time.Time) WindowInfo {
	info := w.info
	info.Pending = 0
	for _, n := range w.notices {
		if (n.Kind == "tool" || n.Kind == "network") && (n.Deadline.IsZero() || now.Before(n.Deadline)) {
			info.Pending++
		}
	}
	return info
}
func (r *Workspaces) ListWindows() []WindowInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]WindowInfo, 0, len(r.windows))
	for _, w := range r.windows {
		result = append(result, r.infoLocked(w, time.Now()))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Title < result[j].Title })
	return result
}
func (r *Workspaces) ShowWindow(id string) error {
	if _, err := r.lookup(id); err != nil {
		return err
	}
	if r.Hooks.Open == nil {
		return fmt.Errorf("open this workspace in its browser window")
	}
	return r.Hooks.Open(id)
}
func (r *Workspaces) HideWindow(id string) error {
	if _, err := r.lookup(id); err != nil {
		return err
	}
	if r.Hooks.Hide == nil {
		return fmt.Errorf("hide-to-tray is available in the native app")
	}
	return r.Hooks.Hide(id)
}

// SetNotifyWhenFocused changes only this window's notification preference.
func (r *Workspaces) SetNotifyWhenFocused(id string, enabled bool) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w := r.windows[id]
	if r.closed || w == nil {
		return false, fmt.Errorf("workspace is unavailable; open a new window")
	}
	w.info.NotifyWhenFocused = enabled
	return enabled, nil
}

func (r *Workspaces) ReadClipboard(id string) (string, error) {
	if _, err := r.lookup(id); err != nil {
		return "", err
	}
	if r.Hooks.ReadClipboard == nil {
		return "", fmt.Errorf("native clipboard is unavailable in browser preview")
	}
	return r.Hooks.ReadClipboard()
}
func (r *Workspaces) WriteClipboard(id, text string) error {
	if _, err := r.lookup(id); err != nil {
		return err
	}
	if r.Hooks.WriteClipboard == nil {
		return fmt.Errorf("native clipboard is unavailable in browser preview")
	}
	return r.Hooks.WriteClipboard(text)
}

func (r *Workspaces) EnableNotifications() (string, error) {
	if r.Hooks.EnableNotifications == nil {
		return "Native notifications require the desktop app", nil
	}
	return r.Hooks.EnableNotifications()
}
func (r *Workspaces) changed() {
	if r.OnChange != nil {
		r.OnChange()
	}
}
func (r *Workspaces) deliver(notices []Notice) {
	if r.OnNotice != nil {
		for _, n := range notices {
			r.OnNotice(n)
		}
	}
}
func withdraw(w *workspace, match func(Notice) bool) []Notice {
	var removed []Notice
	for id, n := range w.notices {
		if match(n) {
			delete(w.notices, id)
			n.Kind = "withdraw"
			removed = append(removed, n)
		}
	}
	return removed
}
func (r *Workspaces) removeNotice(id, token, noticeID string) {
	r.mu.Lock()
	w := r.windows[id]
	var removed []Notice
	if w != nil && w.info.Connection == token {
		removed = withdraw(w, func(n Notice) bool { return n.ID == noticeID })
	}
	r.mu.Unlock()
	r.deliver(removed)
	if len(removed) > 0 {
		r.changed()
	}
}

func (r *Workspaces) Sessions(id string) ([]Sandbox, error) {
	w, err := r.lookup(id)
	if err != nil {
		return nil, err
	}
	return w.service.Sessions()
}
func (r *Workspaces) Connect(id, sandbox string) (string, error) {
	w, err := r.lookup(id)
	if err != nil {
		return "", err
	}
	w.lifecycle.Lock()
	defer w.lifecycle.Unlock()
	// Shutdown may have won while this call waited for the lifecycle lock.
	if _, err = r.lookup(id); err != nil {
		return "", err
	}
	token, err := w.service.Connect(sandbox)
	if err != nil {
		return "", err
	}
	w.service.mu.Lock()
	project := w.service.conn.sandbox.Project
	w.service.mu.Unlock()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		w.service.Disconnect()
		return "", fmt.Errorf("application is shutting down")
	}
	w.info.Connection = token
	w.info.Connected = true
	w.info.Project = project
	w.info.Title = fmt.Sprintf("%s · %d", filepath.Base(project), w.number)
	w.info.AgentStatus = "Not started"
	w.notices = make(map[string]Notice)
	w.notified = make(map[string]bool)
	w.results = make(map[string]bool)
	w.turnFinished = false
	r.mu.Unlock()
	r.changed()
	return token, nil
}
func (r *Workspaces) Disconnect(id string) error {
	w, err := r.lookup(id)
	if err != nil {
		return err
	}
	w.lifecycle.Lock()
	defer w.lifecycle.Unlock()
	r.mu.Lock()
	removed := withdraw(w, func(Notice) bool { return true })
	w.info.Connection = ""
	w.info.Connected = false
	w.info.AgentStatus = "Disconnected"
	r.mu.Unlock()
	w.service.Disconnect()
	r.deliver(removed)
	r.changed()
	return nil
}

//wails:ignore
func (r *Workspaces) Shutdown() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	windows := r.windows
	r.windows = make(map[string]*workspace)
	r.mu.Unlock()
	for _, w := range windows {
		w.lifecycle.Lock()
		w.service.Disconnect()
		r.mu.Lock()
		removed := withdraw(w, func(Notice) bool { return true })
		r.mu.Unlock()
		w.lifecycle.Unlock()
		r.deliver(removed)
	}
}

// IsNoticeActive rejects delayed delivery/click callbacks after resolution.
//
//wails:ignore
func (r *Workspaces) IsNoticeActive(notice Notice) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	w := r.windows[notice.WindowID]
	if r.closed || w == nil || w.info.Connection != notice.Connection {
		return false
	}
	current, ok := w.notices[notice.ID]
	return ok && (current.Deadline.IsZero() || time.Now().Before(current.Deadline))
}

//wails:ignore
func (r *Workspaces) ExpireNotices(now time.Time) {
	r.mu.Lock()
	var removed []Notice
	for _, w := range r.windows {
		removed = append(removed, withdraw(w, func(n Notice) bool { return !n.Deadline.IsZero() && !now.Before(n.Deadline) })...)
	}
	r.mu.Unlock()
	r.deliver(removed)
	if len(removed) > 0 {
		r.changed()
	}
}

func (r *Workspaces) receive(id string, event Event) {
	r.mu.Lock()
	w := r.windows[id]
	if r.closed || w == nil || event.Connection == "" || event.Connection != w.info.Connection {
		r.mu.Unlock()
		return
	}
	event.Workspace = id
	var notices []Notice
	changed := false
	add := func(kind, key, title, body string, deadline time.Time) {
		noticeID := event.Connection + "/" + kind + "/" + key
		n := Notice{ID: noticeID, WindowID: id, Connection: event.Connection, Kind: kind, Title: title, Body: body, Deadline: deadline}
		w.notices[noticeID] = n
		changed = true
		if !w.notified[noticeID] {
			w.notified[noticeID] = true
			notices = append(notices, n)
		}
	}
	remove := func(match func(Notice) bool) {
		removed := withdraw(w, match)
		notices = append(notices, removed...)
		changed = changed || len(removed) > 0
	}
	switch event.Kind {
	case "network-hold":
		if entry, ok := event.Data.(proxy.LogEntry); ok && time.Now().Before(entry.Deadline) {
			body := fmt.Sprintf("%s:%s needs network access. Open Vibepit to review.", entry.Domain, entry.Port)
			remove(func(n Notice) bool {
				return n.Kind == "network" && n.Body == body && n.ID != event.Connection+"/network/"+fmt.Sprint(entry.ID)
			})
			add("network", fmt.Sprint(entry.ID), w.info.Title+": network approval", body, entry.Deadline)
		}
	case "network-end":
		if end, ok := event.Data.(proxy.HoldEnd); ok {
			remove(func(n Notice) bool { return n.ID == event.Connection+"/network/"+fmt.Sprint(end.ID) })
		}
	case "network-reset":
		remove(func(n Notice) bool { return n.Kind == "network" })
	case "agent-config":
		if w.info.AgentStatus == "Not started" {
			w.info.AgentStatus = "Ready"
			changed = true
		}
	case "agent-status":
		if status, ok := event.Data.(string); ok {
			w.info.AgentStatus = status
			changed = true
			if status == "Running" {
				w.turnFinished = false
				w.turn++
				remove(func(n Notice) bool { return n.Kind == "finished" || n.Kind == "error" })
			}
			if status == "Exited" {
				remove(func(n Notice) bool { return n.Kind == "tool" })
				add("error", "exit", w.info.Title+": Claude exited", "Open Vibepit to inspect the connection diagnostics.", time.Time{})
			}
		}
	case "agent-event":
		raw, ok := event.Data.(json.RawMessage)
		if !ok {
			break
		}
		var frame struct {
			Type           string `json:"type"`
			RequestID      string `json:"request_id"`
			UUID           string `json:"uuid"`
			IsError        bool   `json:"is_error"`
			TerminalReason string `json:"terminal_reason"`
			Request        struct {
				Subtype  string `json:"subtype"`
				ToolName string `json:"tool_name"`
			} `json:"request"`
		}
		if json.Unmarshal(raw, &frame) != nil {
			break
		}
		switch frame.Type {
		case "control_request":
			if frame.Request.Subtype == "can_use_tool" {
				add("tool", frame.RequestID, w.info.Title+": Claude needs approval", "Claude wants to use "+frame.Request.ToolName+". Open Vibepit to review.", time.Time{})
			}
		case "control_cancel_request":
			remove(func(n Notice) bool { return n.ID == event.Connection+"/tool/"+frame.RequestID })
		case "result":
			remove(func(n Notice) bool { return n.Kind == "tool" })
			if (frame.UUID != "" && w.results[frame.UUID]) || (frame.UUID == "" && w.turnFinished) {
				break
			}
			w.results[frame.UUID] = true
			w.turnFinished = true
			outcome := "finished"
			body := "Claude finished its turn. Open Vibepit to read the response."
			if frame.IsError {
				outcome = "error"
				body = "Claude reported an error. Open Vibepit to inspect the response."
			} else if frame.TerminalReason == "aborted_tools" || frame.TerminalReason == "aborted_streaming" || frame.TerminalReason == "interrupted" {
				body = "Claude's turn was interrupted."
			} else if frame.TerminalReason != "" && frame.TerminalReason != "completed" {
				body = "Claude stopped: " + frame.TerminalReason + ". Open Vibepit for details."
			}
			key := frame.UUID
			if key == "" {
				key = fmt.Sprintf("turn-%d", w.turn)
			}
			add(outcome, key, w.info.Title+": Claude "+outcome, body, time.Time{})
		}
	}
	r.mu.Unlock()
	if r.emit != nil {
		r.emit(event)
	}
	r.deliver(notices)
	if changed {
		r.changed()
	}
}
