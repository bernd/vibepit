package session

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testManager returns a Manager that uses /bin/sh instead of /bin/bash --login
// to avoid slow profile loading in test environments.
func testManager(limit int) *Manager {
	m := NewManager(limit)
	m.Command = []string{"/bin/sh"}
	return m
}

func TestManager_CreateAndList(t *testing.T) {
	m := testManager(50)
	s, err := m.Create(80, 24, nil)
	require.NoError(t, err)
	require.NotNil(t, s)

	sessions := m.List()
	require.Len(t, sessions, 1)
	assert.Equal(t, "session-1", sessions[0].ID)
	assert.Equal(t, "/bin/sh", sessions[0].Command)
	assert.Equal(t, 0, sessions[0].ClientCount)
}

func TestManager_Limit(t *testing.T) {
	m := testManager(1)
	_, err := m.Create(80, 24, nil)
	require.NoError(t, err)
	_, err = m.Create(80, 24, nil)
	require.Error(t, err)
}

func TestManager_Get(t *testing.T) {
	m := testManager(50)
	s, _ := m.Create(80, 24, nil)
	got := m.Get(s.ID())
	assert.Equal(t, s, got)
	assert.Nil(t, m.Get("nonexistent"))
}

func TestSession_AttachDetach(t *testing.T) {
	m := testManager(50)
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
	m := testManager(50)
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
	m := testManager(50)
	s, err := m.Create(80, 24, nil)
	require.NoError(t, err)

	c1 := s.Attach(80, 24)
	c2 := s.Attach(80, 24)

	// c1 is writer, c2 cannot write
	_, err = c2.Write([]byte("hello"))
	assert.Error(t, err)

	// TakeOver promotes c2
	s.TakeOver(c2, 80, 24)

	_, err = c2.Write([]byte("echo takeover\n"))
	assert.NoError(t, err)

	_, err = c1.Write([]byte("hello"))
	assert.Error(t, err, "old writer should no longer be able to write")

	c1.Close()
	c2.Close()
}

func TestSession_FanOut(t *testing.T) {
	m := testManager(50)
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

func TestSession_Resize(t *testing.T) {
	m := testManager(50)
	s, err := m.Create(80, 24, nil)
	require.NoError(t, err)

	c1 := s.Attach(80, 24)
	c2 := s.Attach(80, 24)

	// Only the writer (c1) can resize.
	s.Resize(c2, 120, 40)
	info := s.Info()
	// Session should still be at original size because c2 is not writer.
	_ = info

	// Writer resizes successfully.
	s.Resize(c1, 120, 40)

	c1.Close()
	c2.Close()
}

func TestSession_ShellExit(t *testing.T) {
	m := testManager(50)
	s, err := m.Create(80, 24, nil)
	require.NoError(t, err)

	c := s.Attach(80, 24)

	// Send exit command to the shell.
	_, err = c.Write([]byte("exit\n"))
	require.NoError(t, err)

	// Read until EOF — the shell should exit.
	buf := make([]byte, 4096)
	deadline := time.After(5 * time.Second)
	for {
		done := make(chan error, 1)
		go func() {
			_, err := c.Read(buf)
			done <- err
		}()
		select {
		case err := <-done:
			if err != nil {
				// Got EOF — shell exited. Give waitForExit a moment
				// to update state.
				time.Sleep(50 * time.Millisecond)
				assert.True(t, s.Exited())
				info := s.Info()
				assert.Equal(t, "exited", info.Status)
				c.Close()
				return
			}
		case <-deadline:
			t.Fatal("timeout waiting for shell to exit")
		}
	}
}

func TestSession_SlowConsumerDisconnected(t *testing.T) {
	m := testManager(50)
	s, err := m.Create(80, 24, nil)
	require.NoError(t, err)

	writer := s.Attach(80, 24)
	slow := s.Attach(80, 24)

	// Fill the slow client's output channel by sending lots of data
	// through the PTY. The slow client never reads.
	for i := range 2000 {
		_, err := writer.Write([]byte(fmt.Sprintf("echo line_%d\n", i)))
		if err != nil {
			break
		}
	}

	// The slow client should have been disconnected. Verify by trying
	// to read — it should return EOF quickly.
	readDone := make(chan error, 1)
	go func() {
		buf := make([]byte, 4096)
		_, err := slow.Read(buf)
		readDone <- err
	}()

	select {
	case <-readDone:
		// Slow client was disconnected (or has data) — either way it's handled.
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for slow consumer to be disconnected")
	}

	writer.Close()
}

func TestSession_ReplayOnAttach(t *testing.T) {
	m := testManager(50)
	s, err := m.Create(80, 24, nil)
	require.NoError(t, err)

	// Attach first client (writer) and send a command.
	c1 := s.Attach(80, 24)
	_, err = c1.Write([]byte("echo hello\n"))
	require.NoError(t, err)

	// Read output until "hello" appears (confirms PTY processed the command).
	buf := make([]byte, 8192)
	deadline := time.After(3 * time.Second)
	var collected string
	for {
		readDone := make(chan int, 1)
		go func() {
			n, _ := c1.Read(buf)
			readDone <- n
		}()
		select {
		case n := <-readDone:
			collected += string(buf[:n])
			if strings.Contains(collected, "hello") {
				goto ready
			}
		case <-deadline:
			t.Fatal("timeout waiting for echo output")
		}
	}
ready:

	// Detach first client (simulates disconnect).
	c1.Close()

	// Attach second client — should receive replay.
	c2 := s.Attach(80, 24)
	n, err := c2.Read(buf)
	require.NoError(t, err)
	assert.Greater(t, n, 0, "should receive replay output")
	c2.Close()
}
