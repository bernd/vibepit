package runtimeenv

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLookupUsesLastValue(t *testing.T) {
	environ := []string{"TERM=xterm", WorkingDir + "=first", WorkingDir + "=nested/project"}

	value, found := Lookup(environ, WorkingDir)

	assert.True(t, found)
	assert.Equal(t, "nested/project", value)
}
