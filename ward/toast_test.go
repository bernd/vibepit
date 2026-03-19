package ward

import (
	"strings"
	"testing"
)

func TestRenderBar(t *testing.T) {
	bar := RenderBar("blocked api.example.com:443", 80)
	if len(bar) == 0 {
		t.Fatal("expected non-empty bar")
	}
	if !strings.Contains(bar, "api.example.com") {
		t.Fatal("bar should contain the message")
	}
}

func TestRenderBarTruncatesLongMessage(t *testing.T) {
	longMsg := strings.Repeat("x", 200)
	bar := RenderBar(longMsg, 80)
	if len(bar) == 0 {
		t.Fatal("expected non-empty bar for long message")
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("hello", 10); got != "hello" {
		t.Fatalf("expected 'hello', got %q", got)
	}
	if got := truncateRunes("hello world", 8); got != "hello..." {
		t.Fatalf("expected 'hello...', got %q", got)
	}
	if got := truncateRunes("", 5); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
	if got := truncateRunes("abc", 0); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}
