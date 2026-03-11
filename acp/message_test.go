package acp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMessageIsRequest(t *testing.T) {
	id := json.RawMessage(`1`)
	msg := Message{JSONRPC: "2.0", ID: &id, Method: "terminal/create"}
	assert.True(t, msg.IsRequest())
	assert.False(t, msg.IsNotification())
	assert.False(t, msg.IsResponse())
}

func TestMessageIsNotification(t *testing.T) {
	msg := Message{JSONRPC: "2.0", Method: "session/update"}
	assert.True(t, msg.IsNotification())
	assert.False(t, msg.IsRequest())
	assert.False(t, msg.IsResponse())
}

func TestMessageIsResponse(t *testing.T) {
	id := json.RawMessage(`1`)
	msg := Message{JSONRPC: "2.0", ID: &id, Result: json.RawMessage(`{}`)}
	assert.True(t, msg.IsResponse())
	assert.False(t, msg.IsRequest())
	assert.False(t, msg.IsNotification())
}

func TestErrorResponse(t *testing.T) {
	id := json.RawMessage(`42`)
	resp := ErrorResponse(&id, -32603, "something went wrong")

	assert.Equal(t, "2.0", resp.JSONRPC)
	assert.NotNil(t, resp.ID)
	assert.Equal(t, -32603, resp.Error.Code)
	assert.Equal(t, "something went wrong", resp.Error.Message)
}

func TestSuccessResponse(t *testing.T) {
	id := json.RawMessage(`42`)
	result := map[string]string{"terminalId": "term-1"}
	resp, err := SuccessResponse(&id, result)
	require.NoError(t, err)

	assert.Equal(t, "2.0", resp.JSONRPC)
	assert.NotNil(t, resp.ID)

	var got map[string]string
	require.NoError(t, json.Unmarshal(resp.Result, &got))
	assert.Equal(t, "term-1", got["terminalId"])
}
