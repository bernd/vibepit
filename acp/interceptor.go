package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
)

// Interceptor bridges an IDE (via stdin/stdout of the vibepit process) and an
// agent (child process), intercepting terminal/* and fs/* methods.
type Interceptor struct {
	agentCmd  string
	agentArgs []string
	terminals *TerminalManager
}

// NewInterceptor creates a new ACP interceptor.
func NewInterceptor(agentCmd string, agentArgs []string) *Interceptor {
	return &Interceptor{
		agentCmd:  agentCmd,
		agentArgs: agentArgs,
		terminals: NewTerminalManager(),
	}
}

// Run starts the agent and runs the message relay loop. It reads JSON-RPC
// messages from ideIn (IDE's stdin→us) and writes to ideOut (us→IDE's stdout).
// It blocks until the agent exits or the context is cancelled.
func (i *Interceptor) Run(ctx context.Context, ideIn io.Reader, ideOut io.Writer) error {
	defer i.terminals.CleanupAll()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := exec.CommandContext(ctx, i.agentCmd, i.agentArgs...)

	agentIn, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("agent stdin pipe: %w", err)
	}
	agentOut, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("agent stdout pipe: %w", err)
	}
	cmd.Stderr = nil // agent stderr is discarded (or could go to our stderr)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start agent: %w", err)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 2)

	// IDE → Agent: forward messages, patching initialize.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer agentIn.Close()
		err := i.relayIDEToAgent(ctx, ideIn, agentIn)
		if err != nil {
			errCh <- fmt.Errorf("IDE→agent: %w", err)
		}
	}()

	// Agent → IDE: forward messages, intercepting terminal/* and fs/*.
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := i.relayAgentToIDE(ctx, agentOut, ideOut, agentIn)
		if err != nil {
			errCh <- fmt.Errorf("agent→IDE: %w", err)
		}
	}()

	// Wait for agent to exit.
	agentErr := cmd.Wait()

	// Cancel context to unblock any goroutines.
	cancel()
	wg.Wait()
	close(errCh)

	// Return agent exit error if any relay errors didn't occur.
	for relayErr := range errCh {
		if relayErr != nil {
			return relayErr
		}
	}

	if agentErr != nil {
		return fmt.Errorf("agent exited: %w", agentErr)
	}
	return nil
}

// relayIDEToAgent reads messages from the IDE and forwards them to the agent.
// It patches the initialize request to add terminal and fs capabilities.
func (i *Interceptor) relayIDEToAgent(ctx context.Context, ideIn io.Reader, agentIn io.Writer) error {
	scanner := bufio.NewScanner(ideIn)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	for scanner.Scan() {
		if ctx.Err() != nil {
			return nil
		}

		line := scanner.Bytes()

		var msg Message
		if err := json.Unmarshal(line, &msg); err != nil {
			// Not valid JSON-RPC — forward as-is.
			if _, err := fmt.Fprintf(agentIn, "%s\n", line); err != nil {
				return err
			}
			continue
		}

		if msg.Method == "initialize" {
			line = patchInitializeCapabilities(line)
		}

		if _, err := fmt.Fprintf(agentIn, "%s\n", line); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// relayAgentToIDE reads messages from the agent and forwards them to the IDE,
// intercepting terminal/* and fs/* requests.
func (i *Interceptor) relayAgentToIDE(ctx context.Context, agentOut io.Reader, ideOut io.Writer, agentIn io.Writer) error {
	scanner := bufio.NewScanner(agentOut)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	for scanner.Scan() {
		if ctx.Err() != nil {
			return nil
		}

		line := scanner.Bytes()

		var msg Message
		if err := json.Unmarshal(line, &msg); err != nil {
			// Not valid JSON-RPC — forward as-is.
			if _, err := fmt.Fprintf(ideOut, "%s\n", line); err != nil {
				return err
			}
			continue
		}

		// Only intercept requests (have method + id).
		if msg.IsRequest() {
			if response := i.handleIntercepted(&msg); response != nil {
				data, err := json.Marshal(response)
				if err != nil {
					return fmt.Errorf("marshal response: %w", err)
				}
				if _, err := fmt.Fprintf(agentIn, "%s\n", data); err != nil {
					return err
				}
				continue
			}
		}

		// Forward everything else to IDE.
		if _, err := fmt.Fprintf(ideOut, "%s\n", line); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// handleIntercepted handles terminal/* and fs/* requests locally.
// Returns nil if the message should be forwarded to the IDE.
func (i *Interceptor) handleIntercepted(msg *Message) *Message {
	switch msg.Method {
	case "terminal/create":
		return i.handleTerminalCreate(msg)
	case "terminal/output":
		return i.handleTerminalOutput(msg)
	case "terminal/wait_for_exit":
		return i.handleTerminalWaitForExit(msg)
	case "terminal/kill":
		return i.handleTerminalKill(msg)
	case "terminal/release":
		return i.handleTerminalRelease(msg)
	case "fs/read_text_file":
		return i.handleFSRead(msg)
	case "fs/write_text_file":
		return i.handleFSWrite(msg)
	default:
		return nil
	}
}

func (i *Interceptor) handleTerminalCreate(msg *Message) *Message {
	var params TerminalCreateParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return ErrorResponse(msg.ID, -32602, fmt.Sprintf("invalid params: %v", err))
	}
	result, err := i.terminals.Create(params)
	if err != nil {
		return ErrorResponse(msg.ID, -32603, err.Error())
	}
	resp, _ := SuccessResponse(msg.ID, result)
	return resp
}

func (i *Interceptor) handleTerminalOutput(msg *Message) *Message {
	var params TerminalOutputParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return ErrorResponse(msg.ID, -32602, fmt.Sprintf("invalid params: %v", err))
	}
	result, err := i.terminals.Output(params)
	if err != nil {
		return ErrorResponse(msg.ID, -32603, err.Error())
	}
	resp, _ := SuccessResponse(msg.ID, result)
	return resp
}

func (i *Interceptor) handleTerminalWaitForExit(msg *Message) *Message {
	var params TerminalWaitParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return ErrorResponse(msg.ID, -32602, fmt.Sprintf("invalid params: %v", err))
	}
	result, err := i.terminals.WaitForExit(params)
	if err != nil {
		return ErrorResponse(msg.ID, -32603, err.Error())
	}
	resp, _ := SuccessResponse(msg.ID, result)
	return resp
}

func (i *Interceptor) handleTerminalKill(msg *Message) *Message {
	var params TerminalKillParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return ErrorResponse(msg.ID, -32602, fmt.Sprintf("invalid params: %v", err))
	}
	if err := i.terminals.Kill(params); err != nil {
		return ErrorResponse(msg.ID, -32603, err.Error())
	}
	resp, _ := SuccessResponse(msg.ID, struct{}{})
	return resp
}

func (i *Interceptor) handleTerminalRelease(msg *Message) *Message {
	var params TerminalReleaseParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return ErrorResponse(msg.ID, -32602, fmt.Sprintf("invalid params: %v", err))
	}
	if err := i.terminals.Release(params); err != nil {
		return ErrorResponse(msg.ID, -32603, err.Error())
	}
	resp, _ := SuccessResponse(msg.ID, struct{}{})
	return resp
}

func (i *Interceptor) handleFSRead(msg *Message) *Message {
	var params FSReadParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return ErrorResponse(msg.ID, -32602, fmt.Sprintf("invalid params: %v", err))
	}
	result, err := ReadFile(params)
	if err != nil {
		return ErrorResponse(msg.ID, -32603, err.Error())
	}
	resp, _ := SuccessResponse(msg.ID, result)
	return resp
}

func (i *Interceptor) handleFSWrite(msg *Message) *Message {
	var params FSWriteParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		return ErrorResponse(msg.ID, -32602, fmt.Sprintf("invalid params: %v", err))
	}
	result, err := WriteFile(params)
	if err != nil {
		return ErrorResponse(msg.ID, -32603, err.Error())
	}
	resp, _ := SuccessResponse(msg.ID, result)
	return resp
}

// patchInitializeCapabilities modifies an initialize request to include
// terminal and fs capabilities in the clientCapabilities.
func patchInitializeCapabilities(line []byte) []byte {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil {
		return line
	}

	paramsRaw, ok := raw["params"]
	if !ok {
		return line
	}

	var params map[string]json.RawMessage
	if err := json.Unmarshal(paramsRaw, &params); err != nil {
		return line
	}

	var caps map[string]json.RawMessage
	if capsRaw, ok := params["clientCapabilities"]; ok {
		if err := json.Unmarshal(capsRaw, &caps); err != nil {
			caps = make(map[string]json.RawMessage)
		}
	} else {
		caps = make(map[string]json.RawMessage)
	}

	caps["terminal"] = json.RawMessage(`true`)
	caps["fs"] = json.RawMessage(`true`)

	capsJSON, err := json.Marshal(caps)
	if err != nil {
		return line
	}
	params["clientCapabilities"] = json.RawMessage(capsJSON)

	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return line
	}
	raw["params"] = json.RawMessage(paramsJSON)

	result, err := json.Marshal(raw)
	if err != nil {
		return line
	}
	return result
}
