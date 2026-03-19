package ward

import "testing"

func TestScreenNewHasCorrectSize(t *testing.T) {
	s := NewScreen(80, 24)
	cols, rows := s.Size()
	if cols != 80 || rows != 24 {
		t.Fatalf("expected 80x24, got %dx%d", cols, rows)
	}
}

func TestScreenWriteAndRender(t *testing.T) {
	s := NewScreen(80, 24)
	s.Write([]byte("hello")) //nolint:errcheck
	rendered := s.Render()
	if len(rendered) == 0 {
		t.Fatal("expected non-empty render")
	}
}

func TestScreenResize(t *testing.T) {
	s := NewScreen(80, 24)
	s.Resize(120, 40)
	cols, rows := s.Size()
	if cols != 120 || rows != 40 {
		t.Fatalf("expected 120x40, got %dx%d", cols, rows)
	}
}

func TestScreenCursorPosition(t *testing.T) {
	s := NewScreen(80, 24)
	s.Write([]byte("ab")) //nolint:errcheck
	col, row := s.CursorPosition()
	// After writing "ab", cursor is at column 2, row 0 (0-indexed)
	if col != 2 || row != 0 {
		t.Fatalf("expected cursor at (2,0), got (%d,%d)", col, row)
	}
}
