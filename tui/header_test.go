package tui_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/bernd/vibepit/tui"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
)

func TestRenderHeader_CompactWhenShort(t *testing.T) {
	header := tui.RenderHeader(&tui.HeaderInfo{
		SessionID:  "abc123",
		ProjectDir: "myproject",
	}, 80, 10)

	plain := ansi.Strip(header)
	lines := strings.Split(header, "\n")
	assert.Equal(t, 1, len(lines), "compact header should be a single line")
	assert.Contains(t, plain, "VIBEPIT")
	assert.Contains(t, plain, "I pity the vibes")
	assert.Contains(t, plain, "abc123")
	assert.Contains(t, plain, "myproject")
}

func TestRenderHeader_FullWhenTall(t *testing.T) {
	header := tui.RenderHeader(&tui.HeaderInfo{
		SessionID:  "abc123",
		ProjectDir: "myproject",
	}, 80, 30)

	lines := strings.Split(header, "\n")
	assert.GreaterOrEqual(t, len(lines), 4, "full header should have at least 4 lines")
}

func TestRenderHeader_CompactAtThreshold(t *testing.T) {
	// height below threshold should be compact
	compact := tui.RenderHeader(&tui.HeaderInfo{
		SessionID:  "abc123",
		ProjectDir: "myproject",
	}, 80, tui.CompactHeaderThreshold-1)
	compactLines := strings.Split(compact, "\n")
	assert.Equal(t, 1, len(compactLines))

	// height at threshold should be full
	full := tui.RenderHeader(&tui.HeaderInfo{
		SessionID:  "abc123",
		ProjectDir: "myproject",
	}, 80, tui.CompactHeaderThreshold)
	fullLines := strings.Split(full, "\n")
	assert.GreaterOrEqual(t, len(fullLines), 4)
}

func TestRenderHeader_CompactFieldFill(t *testing.T) {
	header := tui.RenderHeader(&tui.HeaderInfo{
		SessionID:  "x",
		ProjectDir: "p",
	}, 80, 10)

	plain := ansi.Strip(header)
	// The line should span the full width with field chars filling the gap.
	// Just verify it contains multiple consecutive field chars in the middle.
	assert.Contains(t, plain, "╱╱╱")
}

func TestRenderHeader_ContainsWordmark(t *testing.T) {
	header := tui.RenderHeader(&tui.HeaderInfo{
		SessionID:  "abc123",
		ProjectDir: "/home/user/myproject",
	}, 80, 30)

	plain := ansi.Strip(header)
	lines := strings.Split(header, "\n")
	assert.GreaterOrEqual(t, len(lines), 3, "header should have at least 3 lines")
	assert.Contains(t, plain, "I PITY THE VIBES")
}

func TestRenderHeader_ContainsSessionInfo(t *testing.T) {
	header := tui.RenderHeader(&tui.HeaderInfo{
		SessionID:  "abc123",
		ProjectDir: "/home/user/myproject",
	}, 120, 30)

	plain := ansi.Strip(header)
	assert.Contains(t, plain, "abc123")
	assert.Contains(t, plain, "myproject")
}

func TestRenderBanner_CompactWhenShort(t *testing.T) {
	banner := tui.RenderBanner(80, 10)
	plain := ansi.Strip(banner)
	lines := strings.Split(banner, "\n")
	assert.Equal(t, 1, len(lines), "compact banner should be a single line")
	assert.Contains(t, plain, "VIBEPIT")
	assert.Contains(t, plain, "I pity the vibes")
}

func TestRenderBanner_FullWhenTall(t *testing.T) {
	banner := tui.RenderBanner(80, 30)
	plain := ansi.Strip(banner)
	lines := strings.Split(banner, "\n")
	assert.GreaterOrEqual(t, len(lines), 4, "full banner should have at least 4 lines")
	assert.Contains(t, plain, "I PITY THE VIBES")
}

func TestRenderBanner_NoSessionInfo(t *testing.T) {
	full := tui.RenderBanner(80, 30)
	compact := tui.RenderBanner(80, 10)
	// Banner lines should end with field chars, not session info
	for line := range strings.SplitSeq(full, "\n") {
		plain := ansi.Strip(line)
		if strings.Contains(plain, "PITY") {
			continue // tagline line
		}
		assert.True(t, strings.HasSuffix(plain, "╱"), "wordmark lines should end with field chars")
	}
	_ = compact // compact is tested separately
}

func TestRenderBanner_CompactAtThreshold(t *testing.T) {
	compact := tui.RenderBanner(80, tui.CompactHeaderThreshold-1)
	compactLines := strings.Split(compact, "\n")
	assert.Equal(t, 1, len(compactLines))

	full := tui.RenderBanner(80, tui.CompactHeaderThreshold)
	fullLines := strings.Split(full, "\n")
	assert.GreaterOrEqual(t, len(fullLines), 4)
}

func TestRenderBanner_Print(t *testing.T) {
	t.Skip("visual check only — run with: go test ./tui/ -run TestRenderBanner_Print -v -count=1")
	fmt.Println(tui.RenderBanner(100, 30))
	fmt.Println()
	fmt.Println(tui.RenderBanner(100, 10))
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
