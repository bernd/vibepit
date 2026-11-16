.PHONY: frontend proxy-embed run demo check

ROOT := ../..

# On macOS the root module embeds a Linux vibepit binary for the proxy
# container (embed/proxy/proxy_darwin.go). go:embed fails to compile when it
# is missing, so build it for the host architecture first, like the root
# release-build target does.
proxy-embed:
ifeq ($(shell go env GOHOSTOS),darwin)
	cd $(ROOT) && CGO_ENABLED=0 GOOS=linux GOARCH=$(shell go env GOHOSTARCH) go build -o embed/proxy/vibepit .
endif

frontend:
	cd frontend && npm ci && npm run build

run: frontend proxy-embed
	CGO_ENABLED=0 go run -tags purego .

demo: frontend proxy-embed
	CGO_ENABLED=0 go run -tags server . --demo

check: frontend proxy-embed
	cd frontend && npm run check && npm test
	CGO_ENABLED=0 go test -tags server ./...
	CGO_ENABLED=0 go test -tags purego ./...
