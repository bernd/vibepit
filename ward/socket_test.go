package ward

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSocketPath(t *testing.T) {
	path := SocketPath(12345)
	if filepath.Base(path) != "ward-12345.sock" {
		t.Fatalf("unexpected socket path: %s", path)
	}
}

func TestSocketListenAndNotify(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "test.sock")
	messages := make(chan Notification, 10)

	listener, err := ListenSocket(sockPath, func(n Notification) {
		messages <- n
	})
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer listener.Close() //nolint:errcheck

	err = SendNotification(sockPath, "test message", 5*time.Second)
	if err != nil {
		t.Fatalf("failed to send: %v", err)
	}

	select {
	case n := <-messages:
		if n.Message != "test message" {
			t.Fatalf("expected 'test message', got %q", n.Message)
		}
		if n.Timeout != 5*time.Second {
			t.Fatalf("expected 5s timeout, got %v", n.Timeout)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for message")
	}
}

func TestSocketCleanupOnClose(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "test.sock")

	listener, err := ListenSocket(sockPath, func(n Notification) {})
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	listener.Close() //nolint:errcheck

	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Fatal("socket file should be removed after close")
	}
}

func TestSendNotificationNoListener(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "nonexistent.sock")
	err := SendNotification(sockPath, "hello", DefaultTimeout)
	if err == nil {
		t.Fatal("expected error when no listener")
	}
}

func TestSocketMultipleMessages(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "test.sock")
	messages := make(chan Notification, 10)

	listener, err := ListenSocket(sockPath, func(n Notification) {
		messages <- n
	})
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer listener.Close() //nolint:errcheck

	want := []string{"first", "second", "third"}
	for _, msg := range want {
		if err := SendNotification(sockPath, msg, DefaultTimeout); err != nil {
			t.Fatalf("failed to send %q: %v", msg, err)
		}
	}

	got := make(map[string]bool)
	for range want {
		select {
		case n := <-messages:
			got[n.Message] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for messages, got so far: %v", got)
		}
	}
	for _, expected := range want {
		if !got[expected] {
			t.Fatalf("missing message %q in received set %v", expected, got)
		}
	}
}

func TestSanitizeMessage(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello", "hello"},
		{"hello\x1b[31mworld", "hello[31mworld"},       // strips ESC
		{"hello\x07world", "helloworld"},               // strips BEL
		{"tab\tok", "tab\tok"},                         // preserves tab
		{"\x1b]0;evil title\x07", "]0;evil title"},     // strips ESC and BEL
		{"normal text 123 !@#", "normal text 123 !@#"}, // preserves printable
		{"\x00\x01\x02hello\x7f", "hello"},             // strips C0 and DEL
	}
	for _, tt := range tests {
		got := sanitizeMessage(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeMessage(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestNotificationDefaultTimeout(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "test.sock")
	messages := make(chan Notification, 10)

	listener, err := ListenSocket(sockPath, func(n Notification) {
		messages <- n
	})
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer listener.Close() //nolint:errcheck

	// Send with 10s timeout
	err = SendNotification(sockPath, "hello", 10*time.Second)
	if err != nil {
		t.Fatalf("failed to send: %v", err)
	}

	select {
	case n := <-messages:
		if n.Timeout != 10*time.Second {
			t.Fatalf("expected 10s timeout, got %v", n.Timeout)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for message")
	}
}
