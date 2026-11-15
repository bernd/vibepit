package backend

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/bernd/vibepit/proxy"
	"golang.org/x/crypto/ssh"
)

// This intentionally small adapter follows the control frames used by Anthropic's
// Agent SDKs. It is an experiment, not a replacement for the complete SDK.
type agent struct {
	c             *connection
	session       *ssh.Session
	input         io.WriteCloser
	writeMu       sync.Mutex
	debugMu       sync.Mutex
	debugSequence uint64
	mu            sync.Mutex
	permissions   map[string]json.RawMessage
	config        ClaudeConfig
	configuring   bool
	controls      map[string]chan error
	controlSeq    uint64
	busy          bool
	closed        bool
	ready         chan struct{}
	readyOnce     sync.Once
	initErr       error
	demo          bool
	demoAnswer    chan bool
	demoInterrupt chan struct{}
	demoDone      chan struct{}
	readDone      chan struct{}
	recording     *streamLog
}

func (s *Service) SendPrompt(token, prompt string) error {
	if strings.TrimSpace(prompt) == "" {
		return fmt.Errorf("enter a prompt")
	}
	if len(prompt) > 1024*1024 {
		return fmt.Errorf("prompt exceeds 1 MiB")
	}
	a, err := s.ensureAgent(token, nil)
	if err != nil {
		return err
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return fmt.Errorf("Claude exited")
	}
	if a.busy || a.configuring {
		a.mu.Unlock()
		return fmt.Errorf("wait for the current turn or interrupt it")
	}
	a.busy = true
	if a.demo {
		a.demoAnswer = make(chan bool, 1)
		a.demoInterrupt = make(chan struct{})
		a.demoDone = make(chan struct{})
	}
	a.mu.Unlock()
	a.c.publish("agent-status", "Running")
	err = a.write(map[string]any{
		"type": "user", "session_id": "", "parent_tool_use_id": nil,
		"message": map[string]any{"role": "user", "content": prompt},
	})
	if err != nil {
		a.mu.Lock()
		a.busy = false
		a.mu.Unlock()
	} else if a.demo {
		go a.simulate()
	}
	return err
}

func (s *Service) ensureAgent(token string, startupYOLO *bool) (*agent, error) {
	s.mu.Lock()
	c := s.conn
	if c == nil || c.id != token {
		s.mu.Unlock()
		return nil, fmt.Errorf("connect to a sandbox first")
	}
	a := c.agent
	if a == nil {
		yolo := startupYOLO != nil && *startupYOLO
		a = &agent{config: ClaudeConfig{YOLO: yolo}, c: c, permissions: make(map[string]json.RawMessage), ready: make(chan struct{}), demo: s.demo}
		recording, err := newStreamLog(s.logDir, c.id, c.sandbox)
		if err != nil {
			s.mu.Unlock()
			return nil, fmt.Errorf("create Claude stream recording: %w", err)
		}
		a.recording = recording
		c.publish("agent-log-status", recording.status())
		if err := a.start(); err != nil {
			a.close()
			s.mu.Unlock()
			return nil, err
		}
		c.agent = a
	} else if startupYOLO != nil && a.config.YOLO != *startupYOLO {
		s.mu.Unlock()
		return nil, fmt.Errorf("Claude is already started; disconnect and reconnect to change startup mode")
	}
	s.mu.Unlock()
	select {
	case <-a.ready:
	case <-c.ctx.Done():
		return nil, fmt.Errorf("sandbox disconnected")
	case <-time.After(60 * time.Second):
		a.close()
		return nil, fmt.Errorf("Claude initialization timed out; disconnect and reconnect to retry")
	}
	a.mu.Lock()
	if a.initErr != nil {
		err := a.initErr
		a.mu.Unlock()
		return nil, err
	}
	if a.closed {
		a.mu.Unlock()
		return nil, fmt.Errorf("Claude exited; disconnect and reconnect to start again")
	}
	a.mu.Unlock()
	return a, nil
}

func claudeCommand(yolo bool) string {
	command := "exec claude -p --verbose --input-format stream-json --output-format stream-json --include-partial-messages --permission-prompt-tool stdio"
	if yolo {
		return command + " --dangerously-skip-permissions"
	}
	return command + " --permission-mode default"
}

func (a *agent) start() error {
	a.trace("transport", "Starting Claude: "+claudeCommand(a.config.YOLO))
	if a.demo {
		if err := a.write(controlRequest("initialize", "initialize")); err != nil {
			return err
		}
		a.simulateFrame(map[string]any{"type": "control_response", "response": map[string]any{
			"subtype": "success", "request_id": "initialize", "response": map[string]any{"current_permission_mode": demoPermissionMode(a.config.YOLO), "models": demoModels(), "account": map[string]any{"subscriptionType": "Claude Max (demo)", "apiProvider": "firstParty"}, "commands": []map[string]string{{"name": "review", "description": "Review project changes (simulated)"}, {"name": "compact", "description": "Compact context (simulated)"}, {"name": "explain", "description": "Explain the project (simulated)"}}},
		}})
		return nil
	}
	t, err := a.c.ssh.NewSession()
	if err != nil {
		return err
	}
	input, err := t.StdinPipe()
	if err != nil {
		t.Close()
		return err
	}
	output, err := t.StdoutPipe()
	if err != nil {
		t.Close()
		return err
	}
	t.Stderr = agentErrorWriter{a}
	// No PTY: escape sequences and terminal echo must not contaminate NDJSON.
	err = t.Start(claudeCommand(a.config.YOLO))
	if err != nil {
		t.Close()
		return err
	}
	a.session, a.input = t, input
	a.readDone = make(chan struct{})
	go a.read(output)
	if err := a.write(controlRequest("initialize", "initialize")); err != nil {
		a.close()
		return err
	}
	return nil
}

func controlRequest(id, subtype string) map[string]any {
	return map[string]any{"type": "control_request", "request_id": id, "request": map[string]any{"subtype": subtype}}
}

func (a *agent) initialized(err error) {
	a.readyOnce.Do(func() {
		a.mu.Lock()
		a.initErr = err
		a.mu.Unlock()
		close(a.ready)
	})
}

func (a *agent) read(r io.Reader) {
	defer close(a.readDone)
	defer a.finishRecording()
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := append(json.RawMessage(nil), scanner.Bytes()...)
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		if err := a.handle(line); err != nil {
			a.c.publish("agent-error", err.Error())
			break
		}
	}
	if err := scanner.Err(); err != nil {
		a.c.publish("agent-error", "Claude output: "+err.Error())
	}
	// Closing the channel also releases Wait if parsing failed mid-stream.
	a.session.Close()
	err := a.session.Wait()
	a.initialized(fmt.Errorf("Claude exited before initialization; check the diagnostics and sandbox login"))
	a.mu.Lock()
	a.closed = true
	a.busy = false
	a.permissions = make(map[string]json.RawMessage)
	a.mu.Unlock()
	if err != nil {
		a.c.publish("agent-error", err.Error())
	}
	a.c.publish("agent-status", "Exited")
}

func (a *agent) handle(line json.RawMessage) error {
	// Capture before parsing or routing so malformed and unsupported frames remain visible.
	a.trace("stdout", string(line))
	var frame struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
		Request   struct {
			Subtype string          `json:"subtype"`
			Input   json.RawMessage `json:"input"`
		} `json:"request"`
		Response struct {
			RequestID string          `json:"request_id"`
			Subtype   string          `json:"subtype"`
			Error     string          `json:"error"`
			Response  json.RawMessage `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(line, &frame); err != nil {
		return fmt.Errorf("invalid Claude JSON: %w", err)
	}
	a.observeModel(line)
	switch frame.Type {
	case "control_response":
		var err error
		if frame.Response.Subtype == "error" {
			err = fmt.Errorf("Claude: %s", frame.Response.Error)
		}
		if frame.Response.RequestID == "initialize" {
			if err == nil {
				a.loadModels(frame.Response.Response)
			}
			a.initialized(err)
		} else if !a.controlResponse(frame.Response.RequestID, err) && err != nil {
			a.c.publish("agent-error", err.Error())
		}
	case "control_request":
		if frame.Request.Subtype != "can_use_tool" {
			return a.write(map[string]any{"type": "control_response", "response": map[string]any{
				"subtype": "error", "request_id": frame.RequestID, "error": "Unsupported control request in Vibepit prototype",
			}})
		}
		a.mu.Lock()
		a.permissions[frame.RequestID] = frame.Request.Input
		a.mu.Unlock()
	case "control_cancel_request":
		a.mu.Lock()
		delete(a.permissions, frame.RequestID)
		a.mu.Unlock()
	case "result":
		a.mu.Lock()
		a.busy = false
		a.mu.Unlock()
		a.c.publish("agent-status", "Ready")
	}
	a.c.publish("agent-event", line)
	return nil
}

func (a *agent) write(value any) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	// Trace the attempt before writing: Claude may reply before Write returns.
	a.trace("stdin", string(data))
	if a.demo {
		return nil
	}
	if a.input == nil {
		err = fmt.Errorf("agent input is closed")
	} else {
		var n int
		n, err = a.input.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
	}
	if err != nil {
		a.trace("transport", "stdin write failed: "+err.Error())
	}
	return err
}

type AgentDebugMessage struct {
	Sequence uint64    `json:"sequence"`
	Time     time.Time `json:"time"`
	Stream   string    `json:"stream"`
	Raw      string    `json:"raw"`
}

func (a *agent) trace(stream, raw string) {
	a.debugMu.Lock()
	defer a.debugMu.Unlock()
	a.debugSequence++
	message := AgentDebugMessage{Sequence: a.debugSequence, Time: time.Now(), Stream: stream, Raw: raw}
	if a.recording != nil {
		if err := a.recording.append(message); err != nil {
			a.c.publish("agent-log-status", a.recording.status())
		}
	}
	a.c.publish("agent-debug", message)
}

func (s *Service) DecideTool(token, id string, allow bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil || s.conn.id != token || s.conn.agent == nil {
		return fmt.Errorf("agent is disconnected")
	}
	a := s.conn.agent
	a.mu.Lock()
	input, ok := a.permissions[id]
	if !ok {
		a.mu.Unlock()
		return fmt.Errorf("tool request is no longer pending")
	}
	delete(a.permissions, id)
	answer := a.demoAnswer
	a.mu.Unlock()
	response := map[string]any{"behavior": "deny", "message": "Denied by the user in Vibepit"}
	if allow {
		response = map[string]any{"behavior": "allow", "updatedInput": input}
	}
	err := a.write(map[string]any{"type": "control_response", "response": map[string]any{
		"subtype": "success", "request_id": id, "response": response,
	}})
	if err == nil && a.demo {
		answer <- allow
	}
	return err
}

func (s *Service) InterruptAgent(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil || s.conn.id != token || s.conn.agent == nil {
		return nil
	}
	a := s.conn.agent
	id := fmt.Sprintf("interrupt-%d", time.Now().UnixNano())
	if err := a.write(controlRequest(id, "interrupt")); err != nil {
		return err
	}
	if a.demo {
		a.simulateFrame(map[string]any{"type": "control_response", "response": map[string]any{
			"subtype": "success", "request_id": id, "response": map[string]any{},
		}})
		a.mu.Lock()
		if a.busy && a.demoInterrupt != nil {
			select {
			case <-a.demoInterrupt:
			default:
				close(a.demoInterrupt)
			}
		}
		a.mu.Unlock()
		return nil
	}
	return nil
}

func (a *agent) close() {
	a.mu.Lock()
	a.closed = true
	demoDone := a.demoDone
	a.mu.Unlock()
	if a.session != nil {
		a.session.Close()
	}
	if a.readDone != nil {
		<-a.readDone
	}
	if demoDone != nil {
		<-demoDone
	}
	a.finishRecording()
}

func (a *agent) finishRecording() {
	if a.recording != nil {
		a.recording.close()
		a.c.publish("agent-log-status", a.recording.status())
	}
}

type agentErrorWriter struct{ a *agent }

func (w agentErrorWriter) Write(p []byte) (int, error) {
	w.a.trace("stderr", string(p))
	w.a.c.publish("agent-error", string(p))
	return len(p), nil
}

func (a *agent) simulateFrame(value any) {
	data, err := json.Marshal(value)
	if err == nil {
		err = a.handle(data)
	}
	if err != nil {
		a.c.publish("agent-error", err.Error())
	}
}

func (a *agent) simulate() {
	started := time.Now()
	reason := "aborted_tools"
	messageID := fmt.Sprintf("demo-message-%d", started.UnixNano())
	toolID := fmt.Sprintf("demo-tool-%d", started.UnixNano())
	a.mu.Lock()
	model := a.config.Model
	yolo := a.config.YOLO
	a.mu.Unlock()
	if model == "" {
		model = "sonnet"
	}
	a.simulateFrame(map[string]any{"type": "system", "subtype": "init", "permissionMode": demoPermissionMode(yolo), "model": "demo-" + model, "apiKeySource": "none", "slash_commands": []string{"review", "compact", "explain"}})
	a.mu.Lock()
	interrupted := a.demoInterrupt
	answer := a.demoAnswer
	done := a.demoDone
	a.mu.Unlock()
	if done != nil {
		defer close(done)
	}
	defer func() {
		a.mu.Lock()
		a.busy = false
		a.permissions = make(map[string]json.RawMessage)
		a.mu.Unlock()
		a.simulateFrame(map[string]any{"type": "result", "subtype": "success", "result": "Simulated turn finished.", "terminal_reason": reason, "duration_ms": time.Since(started).Milliseconds(), "usage": map[string]any{"input_tokens": 1200, "cache_read_input_tokens": 15000, "output_tokens": 250}, "modelUsage": map[string]any{"demo-" + model: map[string]any{"contextWindow": 200000}}})
	}()

	a.simulateFrame(map[string]any{"type": "rate_limit_event", "rate_limit_info": map[string]any{"status": "allowed", "isUsingOverage": false, "overageDisabledReason": "org_level_disabled", "unifiedWindows": map[string]any{"five_hour": map[string]any{"utilization": 0.71, "resetsAt": time.Now().Add(2 * time.Hour).Unix()}, "seven_day": map[string]any{"utilization": 0.1, "resetsAt": time.Now().Add(3 * 24 * time.Hour).Unix()}}}})
	a.simulateFrame(map[string]any{"type": "system", "subtype": "status", "status": "requesting"})
	a.simulateFrame(map[string]any{"type": "stream_event", "event": map[string]any{"type": "message_start", "message": map[string]any{"id": messageID, "model": "demo-" + model, "usage": map[string]any{"input_tokens": 1200, "cache_read_input_tokens": 15000}}}})
	a.simulateFrame(map[string]any{"type": "assistant", "message": map[string]any{"id": messageID, "content": []map[string]any{{"type": "tool_use", "id": toolID, "name": "Bash", "input": map[string]any{"command": "git status --short", "description": "Check project changes"}}}}})
	if !yolo {
		a.mu.Lock()
		a.permissions[toolID] = json.RawMessage(`{"command":"git status --short"}`)
		a.mu.Unlock()
		a.simulateFrame(map[string]any{"type": "control_request", "request_id": toolID, "request": map[string]any{
			"subtype": "can_use_tool", "tool_name": "Bash", "tool_use_id": toolID, "input": map[string]any{"command": "git status --short", "description": "Check project changes"},
		}})
		select {
		case <-a.c.ctx.Done():
			return
		case <-interrupted:
			return
		case allowed := <-answer:
			if !allowed {
				reason = "completed"
				a.simulateFrame(map[string]any{"type": "user", "message": map[string]any{"content": []map[string]any{{"type": "tool_result", "tool_use_id": toolID, "content": "Denied by user (demo)", "is_error": true}}}})
				agentErrorWriter{a}.Write([]byte("Simulated tool request denied. No command was executed."))
				return
			}
		}
	}

	a.simulateFrame(map[string]any{"type": "user", "message": map[string]any{"content": []map[string]any{{"type": "tool_result", "tool_use_id": toolID, "content": " M frontend/src/App.svelte (simulated)", "is_error": false}}}, "tool_use_result": map[string]any{"stdout": " M frontend/src/App.svelte (simulated)", "stderr": ""}})
	reason = "aborted_streaming"
	a.simulateFrame(map[string]any{"type": "system", "subtype": "thinking_tokens", "estimated_tokens": 120})
	select {
	case <-a.c.ctx.Done():
		return
	case <-interrupted:
		return
	case <-time.After(800 * time.Millisecond):
	}
	id := uint64(time.Now().UnixMilli())
	a.c.networkEvent(proxy.Event{Type: proxy.EventHoldOpen, Log: proxy.LogEntry{
		ID: id, Time: time.Now(), Domain: "example.com", Port: "443", Source: proxy.SourceProxy,
		Action: proxy.ActionHold, Code: proxy.BlockNotAllowlisted, Deadline: time.Now().Add(45 * time.Second),
	}})
	text := "## Project activity\n\nThis is a **simulated** Claude reply.\n\n- Tool output is grouped under its description.\n- Network requests stay alongside your work.\n\n```sh\ngit status --short\n```\n\nIn live mode, Claude and the shell both run inside your Vibepit sandbox."
	for _, word := range strings.Split(text, " ") {
		select {
		case <-a.c.ctx.Done():
			return
		case <-interrupted:
			return
		case <-time.After(35 * time.Millisecond):
		}
		a.simulateFrame(map[string]any{"type": "stream_event", "event": map[string]any{
			"type": "content_block_delta", "delta": map[string]any{"type": "text_delta", "text": word + " "},
		}})
	}
	a.simulateFrame(map[string]any{"type": "stream_event", "event": map[string]any{"type": "message_delta", "usage": map[string]any{"output_tokens": 250}}})
	reason = "completed"
}

func demoPermissionMode(yolo bool) string {
	if yolo {
		return "bypassPermissions"
	}
	return "default"
}
