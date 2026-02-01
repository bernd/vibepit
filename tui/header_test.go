package tui_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/bernd/vibepit/tui"
	"github.com/stretchr/testify/assert"
)

func TestRenderHeader_ContainsWordmark(t *testing.T) {
	header := tui.RenderHeader(&tui.HeaderInfo{
		SessionID:  "abc123",
		ProjectDir: "/home/user/myproject",
	}, 80)

	lines := strings.Split(header, "\n")
	assert.GreaterOrEqual(t, len(lines), 3, "header should have at least 3 lines")
	assert.Contains(t, header, "I PITY THE VIBES")
}

func TestRenderHeader_ContainsSessionInfo(t *testing.T) {
	header := tui.RenderHeader(&tui.HeaderInfo{
		SessionID:  "abc123",
		ProjectDir: "/home/user/myproject",
	}, 120)

	assert.Contains(t, header, "abc123")
	assert.Contains(t, header, "myproject")
}

func TestRenderHeader_Print(t *testing.T) {
	t.Skip("visual check only — run with: go test ./cmd/ -run TestRenderHeader_Print -v -count=1")
	fmt.Println(tui.RenderHeader(&tui.HeaderInfo{
		SessionID:  "a1b2c3d4e5f6",
		ProjectDir: "/home/user/myproject",
	}, 100))
}
