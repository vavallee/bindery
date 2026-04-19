.PHONY: build dev test lint clean docker-build web-build web-dev help security helm-lint sbom smoke predeploy-smoke

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE    ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS := -ldflags "-w -s -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)"

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

build: web-build ## Build the bindery binary
	cp -r web/dist/* internal/webui/dist/
	CGO_ENABLED=0 go build $(LDFLAGS) -o bindery ./cmd/bindery

dev: ## Run backend in development mode
	go run ./cmd/bindery

web-dev: ## Run frontend dev server
	cd web && npm run dev

web-build: ## Build frontend for embedding
	cd web && npm ci && npm run build

test: ## Run unit tests (use `make smoke` for the HTTP-level smoke suite)
	go test -race -coverprofile=coverage.out -covermode=atomic ./cmd/... ./internal/...

test-web: ## Run frontend tests
	cd web && npm test -- --coverage

lint: ## Run linters
	golangci-lint run ./...
	cd web && npm run lint

lint-go: ## Run Go linter only
	golangci-lint run ./...

lint-web: ## Run frontend linter only
	cd web && npm run lint

docker-build: ## Build Docker image
	docker build -t ghcr.io/vavallee/bindery:$(VERSION) -t ghcr.io/vavallee/bindery:latest .

docker-push: docker-build ## Build and push Docker image
	docker push ghcr.io/vavallee/bindery:$(VERSION)
	docker push ghcr.io/vavallee/bindery:latest

clean: ## Remove build artifacts
	rm -f bindery coverage.out
	rm -rf web/dist web/node_modules

security: ## Run local security scanners (gosec, govulncheck, gitleaks, npm audit)
	@command -v gosec >/dev/null || go install github.com/securego/gosec/v2/cmd/gosec@latest
	@command -v govulncheck >/dev/null || go install golang.org/x/vuln/cmd/govulncheck@latest
	gosec -quiet ./...
	govulncheck ./...
	@if command -v gitleaks >/dev/null; then gitleaks detect --no-banner --redact; \
	 else echo "gitleaks not installed; skipping (brew install gitleaks)"; fi
	cd web && npm audit --audit-level=high || true
	@if command -v trivy >/dev/null; then trivy fs --severity HIGH,CRITICAL --exit-code 1 .; \
	 else echo "trivy not installed; skipping (brew install trivy)"; fi

helm-lint: ## Lint Helm chart + run helm-unittest cases
	helm lint charts/bindery/ --strict
	@if command -v helm-unittest >/dev/null || helm plugin list 2>/dev/null | grep -q unittest; then \
	 helm unittest charts/bindery/; else \
	 echo "helm-unittest not installed; install with: helm plugin install https://github.com/helm-unittest/helm-unittest"; fi

smoke: build ## Boot the real binary and exercise the critical golden paths via HTTP
	go test -count=1 -timeout=60s ./tests/smoke/...

predeploy-smoke: ## Run pre-deploy smoke tests against a live instance (requires BINDERY_URL and BINDERY_API_KEY)
	go test -v -count=1 -timeout=120s ./tests/predeploy/...

sbom: build ## Generate an SPDX SBOM for the local binary
	@command -v syft >/dev/null || (echo "syft not installed; see https://github.com/anchore/syft"; exit 1)
	syft ./bindery -o spdx-json=bindery.sbom.spdx.json
	@echo "SBOM written to bindery.sbom.spdx.json"
