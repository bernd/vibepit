package backend

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func readRecording(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	var records []map[string]any
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var record map[string]any
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &record))
		records = append(records, record)
	}
	require.NoError(t, scanner.Err())
	return records
}

func TestStreamRecordingPreservesFramesAndMetadata(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "streams")
	l, err := newStreamLog(dir, "connection-1", Sandbox{ID: "sandbox-1", Project: "/work/project"})
	require.NoError(t, err)
	defer l.close()
	for i, tc := range []struct{ stream, raw string }{
		{"stdin", "{\"type\":\"user\",\"message\":\"世界\\nnext line\"}\n"},
		{"stdout", ` {"type":"future_message", "unknown":{"keep":true}} `},
		{"stdout", `{"malformed":`},
		{"stderr", "error\nsecond line\n"},
	} {
		require.NoError(t, l.append(AgentDebugMessage{Sequence: uint64(i + 1), Time: time.Now(), Stream: tc.stream, Raw: tc.raw}))
		records := readRecording(t, l.status().Path)
		require.Len(t, records, i+2, "records must be readable before closing the file")
		assert.Equal(t, tc.raw, records[i+1]["raw"])
		assert.Equal(t, tc.stream, records[i+1]["stream"])
		if i < 2 {
			assert.NotNil(t, records[i+1]["message"])
		} else {
			assert.NotContains(t, records[i+1], "message")
		}
	}
	records := readRecording(t, l.status().Path)
	assert.Equal(t, "session", records[0]["kind"])
	assert.Equal(t, "connection-1", records[0]["connection"])
	assert.Equal(t, "/work/project", records[0]["sandbox"].(map[string]any)["project"])
	info, err := os.Stat(l.status().Path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
	other, err := newStreamLog(dir, "connection-2", Sandbox{Demo: true})
	require.NoError(t, err)
	defer other.close()
	assert.NotEqual(t, l.status().Path, other.status().Path)
	assert.Contains(t, other.status().Path, "claude-demo-")
	l.close()
	l.close()
	assert.True(t, l.status().Closed)
	assert.Empty(t, l.status().Error)
	assert.Len(t, readRecording(t, l.status().Path), 5)
}

func TestStreamRecordingSurvivesUIRetentionAndDisconnect(t *testing.T) {
	var status StreamLogStatus
	var mu sync.Mutex
	s := New(true, func(e Event) {
		if e.Kind == "agent-log-status" {
			mu.Lock()
			status = e.Data.(StreamLogStatus)
			mu.Unlock()
		}
	}, t.TempDir())
	token, err := s.Connect("demo")
	require.NoError(t, err)
	require.NoError(t, s.SendPrompt(token, "Record this turn"))
	for i := range 1100 {
		s.conn.agent.trace("stdout", `{"type":"sample","index":`+strconv.Itoa(i)+`}`)
	}
	s.Disconnect()
	mu.Lock()
	path := status.Path
	mu.Unlock()
	records := readRecording(t, path)
	assert.Greater(t, len(records), 1100, "disk recording must not use the UI's 1000-record limit")
	assert.Equal(t, true, records[0]["sandbox"].(map[string]any)["demo"])
	assert.Equal(t, "result", records[len(records)-1]["message"].(map[string]any)["type"], "disconnect must drain the worker before closing the log")
	for i, record := range records[1:] {
		assert.Equal(t, float64(i+1), record["sequence"])
	}
}

func TestStreamRecordingReportsWriteFailure(t *testing.T) {
	l, err := newStreamLog(t.TempDir(), "connection", Sandbox{})
	require.NoError(t, err)
	require.NoError(t, l.file.Close())
	require.Error(t, l.append(AgentDebugMessage{Stream: "stdout", Raw: `{}`}))
	assert.Contains(t, l.status().Error, "recording stopped")
	l.close()
}
