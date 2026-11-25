VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: web build test lint vuln e2e dev clean

# Build the React app into web/dist, which the Go binary embeds.
web:
	cd web && npm install --no-audit --no-fund && npm run typecheck && npm run build

build: web
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/console ./cmd/console

test:
	go test -race ./...

lint:
	test -z "$$(gofmt -l .)" && go vet ./...
	go run honnef.co/go/tools/cmd/staticcheck@latest ./...

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

# Browser tests against the real binary. Set GATEWAY_BIN to also check that
# a real gateway accepts new keys and rejects revoked ones.
e2e: build
	cd web && npm run e2e

# Run locally over plain http with throwaway secrets. Needs users.json.
dev: build
	CONSOLE_INSECURE_COOKIES=true \
	CONSOLE_SESSION_SECRET=dev-session-secret-dev-session-secret \
	CONSOLE_SYNC_TOKEN=dev-sync-token-dev-sync-token-dev-sync \
	PROMETHEUS_URL=$${PROMETHEUS_URL:-http://localhost:9090} \
	bin/console

clean:
	rm -rf bin web/dist/assets web/dist/index.html
