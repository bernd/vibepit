.PHONY: build test test-integration clean release-build release-archive release-publish docs-install docs-build docs-serve

BINARY := vibepit
LDFLAGS := -s -w
VERSION ?= $(shell git describe --tags 2>/dev/null | sed 's/^v//')

PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

export CGO_ENABLED := 0

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

test:
	go test ./...

test-integration:
	go test -tags=integration -timeout 60s ./...

release-build:
	@# Build linux/arm64 proxy binary for darwin embed
	GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/vibepit-linux-arm64 .
	@# Build linux/amd64
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/vibepit-linux-amd64 .
	@# Embed proxy for darwin builds
	mkdir -p embed/proxy
	cp dist/vibepit-linux-arm64 embed/proxy/vibepit
	@# Build darwin/amd64
	GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/vibepit-darwin-amd64 .
	@# Build darwin/arm64
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/vibepit-darwin-arm64 .
	@# Clean embed
	rm -f embed/proxy/vibepit

release-archive:
	@[ -n "$(VERSION)" ] || { echo "VERSION is required"; exit 1; }
	@for platform in $(PLATFORMS); do \
		os="$${platform%/*}"; \
		arch="$${platform#*/}"; \
		case "$$arch" in \
			amd64) arch_name=x86_64 ;; \
			arm64) arch_name=aarch64 ;; \
		esac; \
		name="vibepit-$(VERSION)-$$os-$$arch_name"; \
		echo "Archiving $$name..."; \
		mkdir -p "dist/$$name"; \
		cp "dist/vibepit-$$os-$$arch" "dist/$$name/vibepit"; \
		sed '1,3c# Vibepit' README.md > "dist/$$name/README.md"; \
		cp LICENSE "dist/$$name/"; \
		tar -czf "dist/$$name.tar.gz" -C dist "$$name"; \
		rm -rf "dist/$$name"; \
	done
	cd dist && shasum -a 256 *.tar.gz > checksums.txt

release-publish:
	@[ -n "$(VERSION)" ] || { echo "VERSION is required"; exit 1; }
	gh release create \
		--draft --prerelease --verify-tag \
		--title "v$(VERSION)" \
		v$(VERSION) dist/*.tar.gz dist/checksums.txt

clean:
	rm -f $(BINARY) embed/proxy/vibepit
	rm -rf dist/

docs-install:
	uv sync --project docs

docs-build:
	uv run --project docs mkdocs build --strict

docs-serve:
	uv run --project docs mkdocs serve
