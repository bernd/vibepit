package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRootCommand_AllowCommands(t *testing.T) {
	root := RootCommand()

	names := make(map[string]bool, len(root.Commands))
	for _, c := range root.Commands {
		names[c.Name] = true
	}

	assert.Contains(t, names, "allow-http")
	assert.Contains(t, names, "allow-dns")
	assert.NotContains(t, names, "allow")
}
