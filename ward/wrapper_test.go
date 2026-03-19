package ward

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestWrapperRunsCommandAndExits(t *testing.T) {
	w := NewWrapper(Options{
		Command:    []string{"echo", "hello"},
		SocketPath: SocketPath(os.Getpid()),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	exitCode, err := w.Run(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
}

func TestWrapperExitCodePreserved(t *testing.T) {
	w := NewWrapper(Options{
		Command:    []string{"sh", "-c", "exit 42"},
		SocketPath: SocketPath(os.Getpid()),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	exitCode, err := w.Run(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exitCode != 42 {
		t.Fatalf("expected exit code 42, got %d", exitCode)
	}
}
