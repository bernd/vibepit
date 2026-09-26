package cmd

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPromptNotes(t *testing.T) {
	many := func(n int) []string {
		lines := make([]string, n)
		for i := range lines {
			lines[i] = fmt.Sprintf("note %d", i)
		}
		return lines
	}
	tests := []struct {
		name  string
		lines []string
		want  []string
	}{
		{name: "none"},
		{name: "in order", lines: []string{"a", "b"}, want: []string{"a", "b"}},
		{name: "repeats kept", lines: []string{"a", "a"}, want: []string{"a", "a"}},
		{name: "full", lines: many(maxPromptNotes), want: many(maxPromptNotes)},
		{name: "keeps the last", lines: many(maxPromptNotes + 5), want: many(maxPromptNotes + 5)[5:]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var n promptNotes
			for _, l := range tt.lines {
				n.Printf("%s", l)
			}
			assert.Equal(t, tt.want, n.Lines())
		})
	}

	t.Run("nil holds nothing", func(t *testing.T) {
		var n *promptNotes
		assert.Empty(t, n.Lines())
	})

	t.Run("concurrent", func(t *testing.T) {
		var n promptNotes
		var wg sync.WaitGroup
		for i := range 8 {
			wg.Go(func() { n.Printf("%d", i) })
		}
		wg.Wait()
		assert.Len(t, n.Lines(), 8)
	})
}
