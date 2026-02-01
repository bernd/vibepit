package tui_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/bernd/vibepit/tui"
	"github.com/stretchr/testify/assert"
)

func TestRenderHeader_CompactWhenShort(t *testing.T) {
	header := tui.RenderHeader(&tui.HeaderInfo{
		SessionID:  "abc123",
		ProjectDir: "myproject",
	}, 80, 10)

	lines := strings.Split(strings.TrimPrefix(header, "\n"), "\n")
	assert.Equal(t, 1, len(lines), "compact header should be a single line")
	assert.Contains(t, header, "VIBEPIT")
	assert.Contains(t, header, "I PITY THE VIBES")
	assert.Contains(t, header, "abc123")
	assert.Contains(t, header, "myproject")
}

func TestRenderHeader_FullWhenTall(t *testing.T) {
	header := tui.RenderHeader(&tui.HeaderInfo{
		SessionID:  "abc123",
		ProjectDir: "myproject",
	}, 80, 30)

	lines := strings.Split(strings.TrimPrefix(header, "\n"), "\n")
	assert.GreaterOrEqual(t, len(lines), 4, "full header should have at least 4 lines")
}

func TestRenderHeader_CompactAtThreshold(t *testing.T) {
	// height below threshold should be compact
	compact := tui.RenderHeader(&tui.HeaderInfo{
		SessionID:  "abc123",
		ProjectDir: "myproject",
	}, 80, tui.CompactHeaderThreshold-1)
	compactLines := strings.Split(strings.TrimPrefix(compact, "\n"), "\n")
	assert.Equal(t, 1, len(compactLines))

	// height at threshold should be full
	full := tui.RenderHeader(&tui.HeaderInfo{
		SessionID:  "abc123",
		ProjectDir: "myproject",
	}, 80, tui.CompactHeaderThreshold)
	fullLines := strings.Split(strings.TrimPrefix(full, "\n"), "\n")
	assert.GreaterOrEqual(t, len(fullLines), 4)
}

func TestRenderHeader_CompactFieldFill(t *testing.T) {
	header := tui.RenderHeader(&tui.HeaderInfo{
		SessionID:  "x",
		ProjectDir: "p",
	}, 80, 10)

	// The line should span the full width with field chars filling the gap.
	// Just verify it contains multiple consecutive field chars in the middle.
	assert.Contains(t, header, "╱╱╱")
}

func TestRenderHeader_ContainsWordmark(t *testing.T) {
	header := tui.RenderHeader(&tui.HeaderInfo{
		SessionID:  "abc123",
		ProjectDir: "/home/user/myproject",
	}, 80, 30)

	lines := strings.Split(header, "\n")
	assert.GreaterOrEqual(t, len(lines), 3, "header should have at least 3 lines")
	assert.Contains(t, header, "I PITY THE VIBES")
}

func TestRenderHeader_ContainsSessionInfo(t *testing.T) {
	header := tui.RenderHeader(&tui.HeaderInfo{
		SessionID:  "abc123",
		ProjectDir: "/home/user/myproject",
	}, 120, 30)

	assert.Contains(t, header, "abc123")
	assert.Contains(t, header, "myproject")
}

func TestRenderHeader_Print(t *testing.T) {
	t.Skip("visual check only — run with: go test ./cmd/ -run TestRenderHeader_Print -v -count=1")
	fmt.Println(tui.RenderHeader(&tui.HeaderInfo{
		SessionID:  "a1b2c3d4e5f6",
		ProjectDir: "/home/user/myproject",
	}, 100, 30))
}

func TestRenderHeader_PrintCompact(t *testing.T) {
	t.Skip("visual check only — run with: go test ./tui/ -run TestRenderHeader_PrintCompact -v -count=1")
	fmt.Println(tui.RenderHeader(&tui.HeaderInfo{
		SessionID:  "a1b2c3d4e5f6",
		ProjectDir: "/home/user/myproject",
	}, 100, 10))
}
