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

func TestSetReplacesExistingValues(t *testing.T) {
	environ := []string{"TERM=xterm", WorkingDir + "=first", WorkingDir + "=nested/project"}
	wantInput := append([]string(nil), environ...)

	got := Set(environ, WorkingDir, "/project/nested/project")

	assert.Equal(t, []string{"TERM=xterm", WorkingDir + "=/project/nested/project"}, got)
	assert.Equal(t, wantInput, environ, "input must not be mutated")
}
