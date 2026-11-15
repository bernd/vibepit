.PHONY: frontend run demo check

frontend:
	cd frontend && npm ci && npm run build

run: frontend
	CGO_ENABLED=0 go run -tags purego .

demo: frontend
	CGO_ENABLED=0 go run -tags server . --demo

check: frontend
	cd frontend && npm run check && npm test
	CGO_ENABLED=0 go test -tags server ./...
	CGO_ENABLED=0 go test -tags purego ./...
