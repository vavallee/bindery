.PHONY: build dev test test-race lint clean docker-build web-build web-dev help security helm-lint sbom smoke predeploy-smoke abs-contract check changelog licenses licenses-check go-version-check

# Pinned so local regeneration and the CI drift check classify licenses
# identically — a classifier bump would otherwise look like dependency drift.
GO_LICENSES_VERSION ?= v1.6.0

# Pinned to the same revision the CI govulncheck job installs
# (.github/workflows/ci.yml). `make check` claims to run what the gating CI
# checks run, so it has to resolve the same vulnerability database tooling;
# @latest would let a local run disagree with CI in either direction.
GOVULNCHECK_VERSION ?= d1f380186385b4f64e00313f31743df8e4b89a77

# Local `make security` only; CI runs gosec through golangci-lint.
GOSEC_VERSION ?= v2.29.0

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

# Mirrors the gating CI `test` job, which also runs without -race. The race
# detector lives in `test-race` because internal/api cannot finish under it in
# one timeout budget: CI splits that package into four shards that take 9-12
# minutes each, so a single un-sharded `go test -race ./internal/api` runs past
# any per-package limit and dies in a goroutine dump instead of a test failure
# (#2293). Every CI job runs on ubuntu-latest, so this was only ever hit locally.
test: ## Run unit tests, mirroring the CI gate (no -race; see `make test-race`)
	go test -timeout=15m -coverprofile=coverage.out -covermode=atomic ./cmd/... ./internal/...

# The same six shards, patterns and 15m budget as the `validate` and `race` CI
# matrices in .github/workflows/ci.yml. Keep them in step: a shard pattern that
# drifts here silently stops covering some tests in one place or the other.
test-race: ## Run the race detector in CI's six shards (internal/api overruns a single budget)
	go test -race -timeout=15m -run '^Test[A-B]' ./internal/api
	go test -race -timeout=15m -run '^Test[C-K]' ./internal/api
	go test -race -timeout=15m -run '^Test[L-Q]' ./internal/api
	go test -race -timeout=15m -run '^(Test([^A-Q]|$$)|Fuzz|Example)' ./internal/api
	go test -race -timeout=15m ./internal/db
	pkgs=$$(go list ./cmd/... ./internal/... | grep -v -e '/internal/api$$' -e '/internal/db$$') && \
		test -n "$$pkgs" && go test -race -timeout=15m $$pkgs

test-web: ## Run frontend tests
	cd web && npm test -- --coverage

lint: ## Run linters
	golangci-lint run ./...
	cd web && npm run lint

lint-go: ## Run Go linter only
	golangci-lint run ./...

lint-web: ## Run frontend linter only
	cd web && npm run lint

check: ## Run everything the gating CI checks run (do this before opening a PR)
	go build ./...
	go vet ./...
	golangci-lint run ./...
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...
	$(MAKE) test
	$(MAKE) test-race
	cd web && npm ci && npm run typecheck && npm run lint && npm run build && npm test

changelog: ## Preview the unreleased changelog assembled from changelog.d/*.md
	@found=0; for f in changelog.d/*.md; do \
		case "$$f" in changelog.d/README.md) continue ;; esac; \
		[ -e "$$f" ] || continue; found=1; cat "$$f"; echo; \
	done; [ "$$found" = 1 ] || echo "(no changelog fragments in changelog.d/)"

docker-build: ## Build Docker image
	docker build -t ghcr.io/vavallee/bindery:$(VERSION) -t ghcr.io/vavallee/bindery:latest .

docker-push: docker-build ## Build and push Docker image
	docker push ghcr.io/vavallee/bindery:$(VERSION)
	docker push ghcr.io/vavallee/bindery:latest

clean: ## Remove build artifacts
	rm -f bindery coverage.out
	rm -rf web/dist web/node_modules

security: ## Run local security scanners (gosec, govulncheck, gitleaks, npm audit)
	@command -v gosec >/dev/null || go install github.com/securego/gosec/v2/cmd/gosec@$(GOSEC_VERSION)
	@command -v govulncheck >/dev/null || go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
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

abs-contract: ## Run the pinned ABS contract suite
	go test -count=1 -timeout=15m ./tests/abscontract/...

licenses: ## Regenerate THIRD_PARTY_LICENSES.md (needs web/node_modules)
	@command -v go-licenses >/dev/null || go install github.com/google/go-licenses@$(GO_LICENSES_VERSION)
	go run ./tools/licensegen

licenses-check: ## Fail if THIRD_PARTY_LICENSES.md is stale (the CI drift gate)
	@command -v go-licenses >/dev/null || go install github.com/google/go-licenses@$(GO_LICENSES_VERSION)
	go run ./tools/licensegen -check

go-version-check: ## Fail if a Dockerfile or workflow builds with a Go other than go.mod's toolchain
	@want=$$(sed -n 's/^toolchain go//p' go.mod); \
	if [ -z "$$want" ]; then echo "go.mod has no toolchain directive"; exit 1; fi; \
	bad=0; nd=0; nw=0; \
	dockerfiles=$$(find . \( -name node_modules -o -name vendor -o -name .git -o -path ./.claude \) -prune \
		-o -type f \( -name 'Dockerfile*' -o -name '*.Dockerfile' \) -print); \
	for f in $$dockerfiles; do \
		nd=$$((nd + 1)); \
		for tag in $$(grep -o 'golang:[^@ ]*' "$$f" | sed 's/^golang://'); do \
			got=$${tag%%-*}; \
			if [ "$$got" != "$$want" ]; then echo "$$f: golang:$$tag, go.mod toolchain is go$$want"; bad=1; fi; \
		done; \
	done; \
	for f in .github/workflows/*.yml .github/workflows/*.yaml; do \
		[ -f "$$f" ] || continue; nw=$$((nw + 1)); \
		for got in $$(sed -n 's/^[[:space:]]*go-version:[[:space:]]*"\{0,1\}\([^"[:space:]]*\)"\{0,1\}.*/\1/p' "$$f"); do \
			if [ "$$got" != "$$want" ]; then echo "$$f: go-version $$got, go.mod toolchain is go$$want"; bad=1; fi; \
		done; \
	done; \
	if [ "$$bad" = 1 ]; then echo "Bump go.mod, every Dockerfile golang tag and every setup-go go-version together."; exit 1; fi; \
	echo "Go $$want everywhere: go.mod, $$nd Dockerfiles, $$nw workflows"

sbom: build ## Generate an SPDX SBOM for the local binary
	@command -v syft >/dev/null || (echo "syft not installed; see https://github.com/anchore/syft"; exit 1)
	syft ./bindery -o spdx-json=bindery.sbom.spdx.json
	@echo "SBOM written to bindery.sbom.spdx.json"
