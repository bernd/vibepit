package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDenySet(t *testing.T) {
	var d DenySet
	a443 := Target{Source: SourceProxy, Host: "a.com", Port: "443"}
	assert.False(t, d.Denied(a443))

	d.Add(a443)
	assert.True(t, d.Denied(a443))
	assert.False(t, d.Denied(Target{Source: SourceProxy, Host: "a.com", Port: "80"}), "port is part of the target")
	assert.False(t, d.Denied(Target{Source: SourceDNS, Host: "a.com"}), "sources are separate")

	d.Add(Target{Source: SourceDNS, Host: "b.com"})
	assert.True(t, d.Denied(Target{Source: SourceDNS, Host: "b.com"}))
}
