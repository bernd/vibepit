.PHONY: build build-linux-arm64 build-darwin test test-integration clean

BINARY := vibepit
LDFLAGS := -s -w

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

test:
	go test ./...

test-integration:
	go test -tags=integration -v -timeout 60s ./...

build-linux-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/vibepit-linux-arm64 .

build-darwin: build-linux-arm64
	mkdir -p embed/proxy
	cp dist/vibepit-linux-arm64 embed/proxy/vibepit
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/vibepit-darwin-arm64 .
	rm embed/proxy/vibepit

clean:
	rm -f $(BINARY)
