package acp

import "encoding/json"

// Message represents a JSON-RPC 2.0 message (request, response, or notification).
type Message struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *RPCError        `json:"error,omitempty"`
}

// RPCError represents a JSON-RPC 2.0 error.
type RPCError struct {
	Code    int              `json:"code"`
	Message string           `json:"message"`
	Data    *json.RawMessage `json:"data,omitempty"`
}

// IsRequest returns true if this is a request (has method and id).
func (m *Message) IsRequest() bool {
	return m.Method != "" && m.ID != nil
}

// IsNotification returns true if this is a notification (has method, no id).
func (m *Message) IsNotification() bool {
	return m.Method != "" && m.ID == nil
}

// IsResponse returns true if this is a response (has id, no method).
func (m *Message) IsResponse() bool {
	return m.ID != nil && m.Method == ""
}

// ErrorResponse creates an error response for the given request.
func ErrorResponse(id *json.RawMessage, code int, message string) *Message {
	return &Message{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &RPCError{Code: code, Message: message},
	}
}

// SuccessResponse creates a success response for the given request.
func SuccessResponse(id *json.RawMessage, result any) (*Message, error) {
	data, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	raw := json.RawMessage(data)
	return &Message{
		JSONRPC: "2.0",
		ID:      id,
		Result:  raw,
	}, nil
}
