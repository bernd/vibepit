package ward

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Notification is a message with a display timeout.
type Notification struct {
	Message string
	Timeout time.Duration
}

// DefaultTimeout is the default toast display duration.
const DefaultTimeout = 3 * time.Second

// SocketPath returns the conventional socket path for a given PID.
func SocketPath(pid int) string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("ward-%d.sock", pid))
}

// SocketListener wraps a Unix socket listener that receives notification messages.
type SocketListener struct {
	listener net.Listener
	path     string
}

// ListenSocket creates a Unix socket at the given path. Each incoming connection
// is read line-by-line, parsed as a Notification, and passed to onMessage.
// Wire format: "timeout_seconds;message" or just "message" (uses DefaultTimeout).
func ListenSocket(path string, onMessage func(Notification)) (*SocketListener, error) {
	// Clean up stale socket
	_ = os.Remove(path)

	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", path, err)
	}

	// Restrict socket access to the owner regardless of umask.
	if err := os.Chmod(path, 0600); err != nil {
		l.Close() //nolint:errcheck
		return nil, fmt.Errorf("chmod socket %s: %w", path, err)
	}

	sl := &SocketListener{listener: l, path: path}

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return // listener closed
			}
			go func() {
				defer conn.Close() //nolint:errcheck
				scanner := bufio.NewScanner(conn)
				for scanner.Scan() {
					msg := strings.TrimSpace(scanner.Text())
					if msg != "" {
						onMessage(parseNotification(msg))
					}
				}
			}()
		}
	}()

	return sl, nil
}

// Close stops the listener and removes the socket file.
func (sl *SocketListener) Close() error {
	err := sl.listener.Close()
	_ = os.Remove(sl.path)
	return err
}

// SendNotification connects to the socket and sends a message with a timeout.
// Wire format: "timeout_seconds;message"
func SendNotification(sockPath string, message string, timeout time.Duration) error {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", sockPath, err)
	}
	defer conn.Close() //nolint:errcheck

	secs := int(timeout.Seconds())
	_, err = fmt.Fprintf(conn, "%d;%s\n", secs, message)
	return err
}

// parseNotification parses the wire format "timeout_seconds;message" into a Notification.
// Falls back to DefaultTimeout if the format doesn't match.
// Control characters are stripped from the message to prevent terminal escape injection.
func parseNotification(raw string) Notification {
	var msg string
	timeout := DefaultTimeout

	if idx := strings.IndexByte(raw, ';'); idx > 0 {
		if secs, err := strconv.Atoi(raw[:idx]); err == nil && secs >= 0 {
			msg = raw[idx+1:]
			timeout = time.Duration(secs) * time.Second
		} else {
			msg = raw
		}
	} else {
		msg = raw
	}

	return Notification{Message: sanitizeMessage(msg), Timeout: timeout}
}

// sanitizeMessage strips control characters (C0 and DEL) from a message
// to prevent terminal escape injection via the notification socket.
// Only printable characters, spaces, and tabs are preserved.
func sanitizeMessage(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\t' || (r >= ' ' && r != 0x7F) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
