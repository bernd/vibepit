package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
)

func TestCursor_HandleKey_Down(t *testing.T) {
	c := Cursor{Pos: 2, ItemCount: 10, VpHeight: 5}
	handled := c.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	assert.True(t, handled)
	assert.Equal(t, 3, c.Pos)
}

func TestCursor_HandleKey_Up(t *testing.T) {
	c := Cursor{Pos: 3, ItemCount: 10, VpHeight: 5}
	handled := c.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	assert.True(t, handled)
	assert.Equal(t, 2, c.Pos)
}

func TestCursor_HandleKey_JumpEnd(t *testing.T) {
	c := Cursor{Pos: 0, ItemCount: 10, VpHeight: 5}
	c.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	assert.Equal(t, 9, c.Pos)
}

func TestCursor_HandleKey_JumpStart(t *testing.T) {
	c := Cursor{Pos: 8, ItemCount: 10, VpHeight: 5}
	c.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	assert.Equal(t, 0, c.Pos)
}

func TestCursor_HandleKey_PageDown(t *testing.T) {
	c := Cursor{Pos: 0, ItemCount: 20, VpHeight: 5}
	c.HandleKey(tea.KeyMsg{Type: tea.KeyPgDown})
	assert.Equal(t, 5, c.Pos)
}

func TestCursor_HandleKey_PageUp(t *testing.T) {
	c := Cursor{Pos: 8, ItemCount: 20, VpHeight: 5}
	c.HandleKey(tea.KeyMsg{Type: tea.KeyPgUp})
	assert.Equal(t, 3, c.Pos)
}

func TestCursor_HandleKey_BoundsDown(t *testing.T) {
	c := Cursor{Pos: 9, ItemCount: 10, VpHeight: 5}
	c.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	assert.Equal(t, 9, c.Pos)
}

func TestCursor_HandleKey_BoundsUp(t *testing.T) {
	c := Cursor{Pos: 0, ItemCount: 10, VpHeight: 5}
	c.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	assert.Equal(t, 0, c.Pos)
}

func TestCursor_HandleKey_UnknownKey(t *testing.T) {
	c := Cursor{Pos: 3, ItemCount: 10, VpHeight: 5}
	handled := c.HandleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	assert.False(t, handled)
	assert.Equal(t, 3, c.Pos)
}

func TestCursor_EnsureVisible(t *testing.T) {
	c := Cursor{Pos: 12, Offset: 0, VpHeight: 5, ItemCount: 20}
	c.EnsureVisible()
	assert.Equal(t, 8, c.Offset)
}

func TestCursor_AtEnd(t *testing.T) {
	c := Cursor{Pos: 9, ItemCount: 10}
	assert.True(t, c.AtEnd())
	c.Pos = 5
	assert.False(t, c.AtEnd())
}

func TestCursor_FooterKeys(t *testing.T) {
	c := Cursor{}
	keys := c.FooterKeys()
	assert.Len(t, keys, 2)
}
