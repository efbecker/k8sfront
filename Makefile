# k8sfront — common developer tasks. Run `make help` for the list.

API_DIR := apps/api
WEB_DIR := apps/web
BIN     := $(API_DIR)/bin/k8sfront-api
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

.PHONY: deps
deps: ## Install Go and npm dependencies
	cd $(API_DIR) && go mod download
	npm ci

.PHONY: build
build: build-web build-api ## Build frontend and backend

.PHONY: build-api
build-api: ## Build the Go control plane binary
	cd $(API_DIR) && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/efbecker/k8sfront/apps/api/internal/api.Version=$(VERSION)" -o bin/k8sfront-api ./cmd/k8sfront-api

.PHONY: build-web
build-web: ## Build the React frontend into apps/web/dist
	npm run build -w $(WEB_DIR)

.PHONY: dist
dist: build-web ## Release tarballs (linux amd64/arm64) + SHA256SUMS in dist/, used by install.sh
	rm -rf dist && mkdir -p dist
	set -e; for arch in amd64 arm64; do \
	  stage=dist/stage-$$arch/k8sfront; mkdir -p $$stage/bin $$stage/web; \
	  (cd $(API_DIR) && CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -ldflags "-s -w -X github.com/efbecker/k8sfront/apps/api/internal/api.Version=$(VERSION)" -o ../../$$stage/bin/k8sfront-api ./cmd/k8sfront-api); \
	  cp -r $(WEB_DIR)/dist/. $$stage/web/; cp install.sh LICENSE* README.md $$stage/ 2>/dev/null || true; \
	  tar -C dist/stage-$$arch -czf dist/k8sfront_$(VERSION)_linux_$$arch.tar.gz k8sfront; \
	done
	rm -rf dist/stage-*
	cd dist && sha256sum k8sfront_*.tar.gz > SHA256SUMS

.PHONY: run
run: build ## Build everything and serve the SPA from the API on :8080
	K8SFRONT_WEB_DIR=$(WEB_DIR)/dist ./$(BIN)

.PHONY: dev-api
dev-api: ## Run the API in development mode (data in ./data)
	cd $(API_DIR) && K8SFRONT_DATA_DIR=../../data K8SFRONT_LOG_LEVEL=debug K8SFRONT_ALLOWED_ORIGINS=localhost:5173 go run ./cmd/k8sfront-api

.PHONY: dev-web
dev-web: ## Run the Vite dev server on :5173 (proxies /api to :8080)
	npm run dev -w $(WEB_DIR)

.PHONY: test
test: test-api test-web ## Run unit tests

.PHONY: test-api
test-api: ## Go unit tests with the race detector
	cd $(API_DIR) && go test -race ./...

.PHONY: test-integration
test-integration: ## SSH/sudo integration tests (root + sudo; Docker + debian:trixie for the Debian 13 e2e)
	cd $(API_DIR) && go test -tags integration -race -count=1 ./test/integration/...

.PHONY: test-web
test-web: ## Frontend unit tests
	npm run test -w $(WEB_DIR)

.PHONY: lint
lint: ## gofmt + go vet + TypeScript typecheck
	@test -z "$$(gofmt -l $(API_DIR))" || (gofmt -l $(API_DIR); echo "run gofmt -w $(API_DIR)"; exit 1)
	cd $(API_DIR) && go vet ./... && go vet -tags integration ./test/...
	npm run typecheck -w $(WEB_DIR)

.PHONY: clean
clean: ## Remove build outputs
	rm -rf $(API_DIR)/bin $(WEB_DIR)/dist dist
