package backend

func (r *Workspaces) OpenTerminal(workspaceID, token string, cols, rows int) error {
	service, err := r.session(workspaceID, token)
	if err != nil {
		return err
	}
	return service.OpenTerminal(token, cols, rows)
}

func (r *Workspaces) WriteTerminal(workspaceID, token string, data string) error {
	service, err := r.session(workspaceID, token)
	if err != nil {
		return err
	}
	return service.WriteTerminal(token, data)
}

func (r *Workspaces) WriteTerminalBytes(workspaceID, token string, data string) error {
	service, err := r.session(workspaceID, token)
	if err != nil {
		return err
	}
	return service.WriteTerminalBytes(token, data)
}

func (r *Workspaces) ResizeTerminal(workspaceID, token string, cols, rows int) error {
	service, err := r.session(workspaceID, token)
	if err != nil {
		return err
	}
	return service.ResizeTerminal(token, cols, rows)
}

func (r *Workspaces) WatchNetwork(workspaceID, token string) error {
	service, err := r.session(workspaceID, token)
	if err != nil {
		return err
	}
	return service.WatchNetwork(token)
}

func (r *Workspaces) DecideNetwork(workspaceID, token string, requestID uint64, allow bool) error {
	service, err := r.session(workspaceID, token)
	if err != nil {
		return err
	}
	return service.DecideNetwork(token, requestID, allow)
}

func (r *Workspaces) StartClaude(workspaceID, token string, yolo bool) (ClaudeConfig, error) {
	service, err := r.session(workspaceID, token)
	if err != nil {
		return ClaudeConfig{}, err
	}
	return service.StartClaude(token, yolo)
}

func (r *Workspaces) SetClaudeOption(workspaceID, token string, option, value string) (ClaudeConfig, error) {
	service, err := r.session(workspaceID, token)
	if err != nil {
		return ClaudeConfig{}, err
	}
	return service.SetClaudeOption(token, option, value)
}

func (r *Workspaces) SendPrompt(workspaceID, token string, prompt string) error {
	service, err := r.session(workspaceID, token)
	if err != nil {
		return err
	}
	return service.SendPrompt(token, prompt)
}

func (r *Workspaces) DecideTool(workspaceID, token string, requestID string, allow bool) error {
	service, err := r.session(workspaceID, token)
	if err != nil {
		return err
	}
	if err := service.DecideTool(token, requestID, allow); err != nil {
		return err
	}
	r.removeNotice(workspaceID, token, token+"/tool/"+requestID)
	return nil
}

func (r *Workspaces) InterruptAgent(workspaceID, token string) error {
	service, err := r.session(workspaceID, token)
	if err != nil {
		return err
	}
	return service.InterruptAgent(token)
}
