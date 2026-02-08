//go:build darwin

package proxy

import "embed"

//go:embed vibepit*
var proxyFS embed.FS
