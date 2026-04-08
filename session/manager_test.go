package session

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManager_CreateAndList(t *testing.T) {
	m := NewManager(50)
	s, err := m.Create(80, 24, nil)
	require.NoError(t, err)
	require.NotNil(t, s)

	sessions := m.List()
	require.Len(t, sessions, 1)
	assert.Equal(t, "session-1", sessions[0].ID)
	assert.Equal(t, "/bin/bash", sessions[0].Command)
	assert.Equal(t, 0, sessions[0].ClientCount)
}

func TestManager_Limit(t *testing.T) {
	m := NewManager(1)
	_, err := m.Create(80, 24, nil)
	require.NoError(t, err)
	_, err = m.Create(80, 24, nil)
	require.Error(t, err)
}

func TestManager_Get(t *testing.T) {
	m := NewManager(50)
	s, _ := m.Create(80, 24, nil)
	got := m.Get(s.ID())
	assert.Equal(t, s, got)
	assert.Nil(t, m.Get("nonexistent"))
}

func TestSession_AttachDetach(t *testing.T) {
	m := NewManager(50)
	s, err := m.Create(80, 24, nil)
	require.NoError(t, err)

	c1 := s.Attach(80, 24)
	require.NotNil(t, c1)

	info := s.Info()
	assert.Equal(t, 1, info.ClientCount)
	assert.Equal(t, "attached", info.Status)

	c1.Close()

	info = s.Info()
	assert.Equal(t, 0, info.ClientCount)
	assert.Equal(t, "detached", info.Status)
}

func TestSession_WriterPromotion(t *testing.T) {
	m := NewManager(50)
	s, err := m.Create(80, 24, nil)
	require.NoError(t, err)

	c1 := s.Attach(80, 24)
	c2 := s.Attach(80, 24)

	// c1 is the writer
	_, err = c2.Write([]byte("hello"))
	assert.Error(t, err, "non-writer should not be able to write")

	_, err = c1.Write([]byte("echo hi\n"))
	assert.NoError(t, err, "writer should be able to write")

	// Detach writer, c2 should be promoted
	c1.Close()

	// Give a moment for state to settle
	time.Sleep(10 * time.Millisecond)

	_, err = c2.Write([]byte("echo promoted\n"))
	assert.NoError(t, err, "promoted client should be able to write")

	c2.Close()
}

func TestSession_TakeOver(t *testing.T) {
	m := NewManager(50)
	s, err := m.Create(80, 24, nil)
	require.NoError(t, err)

	c1 := s.Attach(80, 24)
	c2 := s.Attach(80, 24)

	// c1 is writer, c2 cannot write
	_, err = c2.Write([]byte("hello"))
	assert.Error(t, err)

	// TakeOver promotes c2
	s.TakeOver(c2)

	_, err = c2.Write([]byte("echo takeover\n"))
	assert.NoError(t, err)

	_, err = c1.Write([]byte("hello"))
	assert.Error(t, err, "old writer should no longer be able to write")

	c1.Close()
	c2.Close()
}

func TestSession_FanOut(t *testing.T) {
	m := NewManager(50)
	s, err := m.Create(80, 24, nil)
	require.NoError(t, err)

	c1 := s.Attach(80, 24)
	c2 := s.Attach(80, 24)

	// Write something that produces output
	_, err = c1.Write([]byte("echo fanout_test_123\n"))
	require.NoError(t, err)

	// Both clients should receive output
	buf1 := make([]byte, 4096)
	buf2 := make([]byte, 4096)

	// Read with timeout
	done := make(chan struct{})
	var n1, n2 int
	go func() {
		n1, _ = c1.Read(buf1)
		close(done)
	}()

	select {
	case <-done:
		assert.Greater(t, n1, 0)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout reading from c1")
	}

	done2 := make(chan struct{})
	go func() {
		n2, _ = c2.Read(buf2)
		close(done2)
	}()

	select {
	case <-done2:
		assert.Greater(t, n2, 0)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout reading from c2")
	}

	c1.Close()
	c2.Close()
}

func TestSession_ReplayOnAttach(t *testing.T) {
	m := NewManager(50)
	s, err := m.Create(80, 24, nil)
	require.NoError(t, err)

	// Attach first client (writer) and send a command.
	c1 := s.Attach(80, 24)
	_, err = c1.Write([]byte("echo hello\n"))
	require.NoError(t, err)

	// Wait for output to flow through PTY.
	time.Sleep(300 * time.Millisecond)

	// Detach first client (simulates disconnect).
	c1.Close()

	// Attach second client — should receive replay.
	c2 := s.Attach(80, 24)
	buf := make([]byte, 8192)
	n, err := c2.Read(buf)
	require.NoError(t, err)
	assert.Greater(t, n, 0, "should receive replay output")
	c2.Close()
}
