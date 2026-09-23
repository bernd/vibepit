package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDenySet(t *testing.T) {
	var d DenySet
	assert.False(t, d.Denied(SourceProxy, "a.com:443"))

	d.Add(SourceProxy, "a.com:443")
	assert.True(t, d.Denied(SourceProxy, "a.com:443"))
	assert.False(t, d.Denied(SourceProxy, "a.com:80"), "port is part of the target")
	assert.False(t, d.Denied(SourceDNS, "a.com:443"), "sources are separate")

	d.Add(SourceDNS, "b.com")
	assert.True(t, d.Denied(SourceDNS, "b.com"))
}
