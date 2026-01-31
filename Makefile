.PHONY: build test test-integration clean

BINARY := vibepit
LDFLAGS := -s -w

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

test:
	go test ./...

test-integration:
	go test -tags=integration -v -timeout 60s ./...

clean:
	rm -f $(BINARY)
