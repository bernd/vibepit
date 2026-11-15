package backend

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

type ClaudeModel struct {
	Value                 string   `json:"value"`
	DisplayName           string   `json:"displayName"`
	ResolvedModel         string   `json:"resolvedModel,omitempty"`
	SupportsEffort        *bool    `json:"supportsEffort,omitempty"`
	SupportedEffortLevels []string `json:"supportedEffortLevels,omitempty"`
}

// Model and Effort are acknowledged session overrides, not guesses at inherited
// settings or the API's effective effort (which may be capped by policy).
type ClaudeConfig struct {
	YOLO           bool          `json:"yolo"`
	PermissionMode string        `json:"permissionMode,omitempty"`
	Models         []ClaudeModel `json:"models"`
	Model          string        `json:"model"`
	ReportedModel  string        `json:"reportedModel"`
	Effort         string        `json:"effort"`
	EffortReset    bool          `json:"effortReset"`
}

func (s *Service) StartClaude(token string, yolo bool) (ClaudeConfig, error) {
	a, err := s.ensureAgent(token, &yolo)
	if err != nil {
		return ClaudeConfig{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.config, nil
}

// Switches are allowed only between turns so a prompt cannot race its settings.
func (s *Service) SetClaudeOption(token, option, value string) (ClaudeConfig, error) {
	a, err := s.ensureAgent(token, nil)
	if err != nil {
		return ClaudeConfig{}, err
	}
	a.mu.Lock()
	if a.closed || a.busy || a.configuring {
		a.mu.Unlock()
		return ClaudeConfig{}, fmt.Errorf("wait until Claude is idle before changing settings")
	}
	request := map[string]any{}
	switch option {
	case "model":
		if value != "" && !slices.ContainsFunc(a.config.Models, func(m ClaudeModel) bool { return m.Value == value }) {
			a.mu.Unlock()
			return ClaudeConfig{}, fmt.Errorf("model was not advertised by Claude")
		}
		request["subtype"] = "set_model"
		if value != "" {
			request["model"] = value
		}
	case "effort":
		levels := []string{"low", "medium", "high"}
		for _, model := range a.config.Models {
			if (a.config.Model != "" && model.Value == a.config.Model) || (a.config.Model == "" && model.ResolvedModel != "" && model.ResolvedModel == a.config.ReportedModel) {
				if model.SupportsEffort != nil && !*model.SupportsEffort {
					levels = nil
				} else if len(model.SupportedEffortLevels) > 0 {
					levels = model.SupportedEffortLevels
				}
				break
			}
		}
		if value != "" && !slices.Contains(levels, value) {
			a.mu.Unlock()
			return ClaudeConfig{}, fmt.Errorf("effort is not supported by this model")
		}
		var effort any
		if value != "" {
			effort = value
		}
		request = map[string]any{"subtype": "apply_flag_settings", "settings": map[string]any{"effortLevel": effort}}
	default:
		a.mu.Unlock()
		return ClaudeConfig{}, fmt.Errorf("unknown Claude setting")
	}
	a.configuring = true
	a.controlSeq++
	id := fmt.Sprintf("config-%d", a.controlSeq)
	response := make(chan error, 1)
	if a.controls == nil {
		a.controls = make(map[string]chan error)
	}
	a.controls[id] = response
	a.mu.Unlock()
	defer func() { a.mu.Lock(); a.configuring = false; delete(a.controls, id); a.mu.Unlock() }()
	if err := a.write(map[string]any{"type": "control_request", "request_id": id, "request": request}); err != nil {
		// A partial write may have applied. Close rather than claim the old state.
		a.close()
		a.c.publish("agent-status", "Exited")
		return ClaudeConfig{}, err
	}
	if a.demo {
		a.simulateFrame(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": id, "response": map[string]any{}}})
	}
	select {
	case err := <-response:
		if err != nil {
			return ClaudeConfig{}, err
		}
	case <-a.c.ctx.Done():
		return ClaudeConfig{}, fmt.Errorf("sandbox disconnected")
	case <-a.readDone:
		return ClaudeConfig{}, fmt.Errorf("Claude exited before confirming the setting")
	case <-time.After(15 * time.Second):
		a.close()
		a.c.publish("agent-status", "Exited")
		return ClaudeConfig{}, fmt.Errorf("Claude did not confirm the setting; reconnect before continuing")
	}
	a.mu.Lock()
	if option == "model" {
		a.config.Model = value
	} else {
		a.config.Effort = value
		a.config.EffortReset = value == ""
	}
	config := a.config
	a.mu.Unlock()
	a.c.publish("agent-config", config)
	return config, nil
}

func (a *agent) controlResponse(id string, err error) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	ch, ok := a.controls[id]
	if ok {
		delete(a.controls, id)
		ch <- err
	}
	return ok
}

func (a *agent) loadModels(raw json.RawMessage) {
	var response struct {
		Models         []ClaudeModel `json:"models"`
		PermissionMode string        `json:"current_permission_mode"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return
	}
	a.mu.Lock()
	a.config.Models = response.Models
	a.config.PermissionMode = response.PermissionMode
	config := a.config
	a.mu.Unlock()
	a.c.publish("agent-config", config)
}

func (a *agent) observeModel(raw json.RawMessage) {
	var frame struct {
		Type           string  `json:"type"`
		PermissionMode string  `json:"permissionMode"`
		Subtype        string  `json:"subtype"`
		Model          string  `json:"model"`
		Parent         *string `json:"parent_tool_use_id"`
		Message        struct {
			Model string `json:"model"`
		} `json:"message"`
		Event struct {
			Type    string `json:"type"`
			Message struct {
				Model string `json:"model"`
			} `json:"message"`
		} `json:"event"`
	}
	if json.Unmarshal(raw, &frame) != nil || frame.Parent != nil {
		return
	}
	model := ""
	if frame.Type == "system" && frame.Subtype == "init" {
		model = frame.Model
	}
	if frame.Type == "assistant" {
		model = frame.Message.Model
	}
	if frame.Type == "stream_event" && frame.Event.Type == "message_start" {
		model = frame.Event.Message.Model
	}
	permissionMode := ""
	if frame.Type == "system" && frame.Subtype == "init" {
		permissionMode = frame.PermissionMode
	}
	if model == "<synthetic>" {
		model = ""
	}
	if model == "" && permissionMode == "" {
		return
	}
	a.mu.Lock()
	changed := (model != "" && a.config.ReportedModel != model) || (permissionMode != "" && a.config.PermissionMode != permissionMode)
	if model != "" {
		a.config.ReportedModel = model
	}
	if permissionMode != "" {
		a.config.PermissionMode = permissionMode
	}
	config := a.config
	a.mu.Unlock()
	if changed {
		a.c.publish("agent-config", config)
	}
}

func demoModels() []ClaudeModel {
	yes, no := true, false
	return []ClaudeModel{
		{Value: "sonnet", DisplayName: "Sonnet (demo)", ResolvedModel: "demo-sonnet", SupportsEffort: &yes, SupportedEffortLevels: []string{"low", "medium", "high"}},
		{Value: "opus", DisplayName: "Opus (demo)", ResolvedModel: "demo-opus", SupportsEffort: &yes, SupportedEffortLevels: []string{"low", "medium", "high", "max"}},
		{Value: "haiku", DisplayName: "Haiku (demo)", ResolvedModel: "demo-haiku", SupportsEffort: &no},
	}
}
