# falco — image processing and delivery service.
#
# `make check` is what has to pass before a commit; the lint gate
# (.golangci.yml) is what keeps readability from degrading again.
# `make help` lists every target.

BINARY_NAME  := falco-server
DOCKER_IMAGE := falco-service
VERSION      ?= dev
COMMIT       ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS      := -X github.com/birdple/falco/internal/version.Version=$(VERSION) \
                -X github.com/birdple/falco/internal/version.Commit=$(COMMIT)

# Kept in step with scripts/lint-in-container.sh and .github/workflows/ci.yml.
GOLANGCI_LINT_VERSION := v2.13.2

.DEFAULT_GOAL := help

# ---------------------------------------------------------------------------
# Gate
# ---------------------------------------------------------------------------

.PHONY: check check-fmt check-build
check: check-fmt vet lint test ui-check mocks-check check-build ## Everything that must pass before a commit
	@echo "ok: check passed"

check-fmt: ## Fail if anything is not gofmt-formatted
	@test -z "$$(gofmt -l . | grep -v vendor)" || \
		(echo "not formatted:"; gofmt -l . | grep -v vendor; exit 1)

check-build: ## Compile EVERY package for the host
	@# Not the `build` target: that one cross-compiles to Linux with CGO and
	@# does not run on macOS, where libvips comes from the host.
	go build ./...

# ---------------------------------------------------------------------------
# Build and run
# ---------------------------------------------------------------------------

.PHONY: build build-local run dev
build: ## Build the Linux binary (CGO; run it on Linux)
	CGO_ENABLED=1 GOOS=linux go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY_NAME) ./cmd/server

build-local: ## Build the binary for the host
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY_NAME) ./cmd/server

run: ## Run the service (needs libvips on the host)
	go run ./cmd/server

# air reads .air.toml from the root if it exists; without -c it does not fail
# when there is none.
dev: ## Run with hot reload (needs air, see `make setup`)
	air

# ---------------------------------------------------------------------------
# Tests and quality
# ---------------------------------------------------------------------------

.PHONY: test test-coverage test-performance lint lint-linux fmt vet vuln
test: ## Run all tests with the race detector, as CI does
	go test -race ./...

test-coverage: ## Run the tests with an HTML coverage report
	go test -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

test-performance: ## Run the benchmarks
	go test -run '^$$' -bench=. -benchmem ./...

lint: ## Run golangci-lint on the host
	golangci-lint run

# The host lint is NOT CI's lint: some rules depend on the platform (unconvert
# on syscall.Statfs_t, whose field type differs between Linux and Darwin).
# This runs the same golangci-lint as the workflow, inside Linux.
lint-linux: ## Run CI's lint inside a Linux container
	@scripts/lint-in-container.sh

fmt: ## Format the code
	gofmt -w .

vet: ## Run go vet
	go vet ./...

vuln: ## Check dependencies for known vulnerabilities
	go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...

# ---------------------------------------------------------------------------
# Generated code
#
# The panel templates and CSS are COMPILED, and the test mocks are GENERATED.
# Without these targets, editing a .templ, a Tailwind class or an interface
# changes nothing the binary or the tests see, and nothing notices: the
# *_templ.go files once said v0.3.1001 while go.mod required v0.3.1020.
# ---------------------------------------------------------------------------

TAILWIND_VERSION := 4.2.2
TAILWIND_BIN     := bin/tailwindcss

.PHONY: ui ui-templ ui-css ui-check mocks mocks-check
ui: ui-templ ui-css ## Regenerate the panel: templ templates + Tailwind CSS

ui-templ: ## Generate the *_templ.go files from the .templ ones
	@go tool templ generate

ui-css: $(TAILWIND_BIN) ## Compile web/static/css/input.css -> output.css
	@$(TAILWIND_BIN) -i web/static/css/input.css -o web/static/css/output.css

# bin/ is in .gitignore, so a clean clone has no Tailwind compiler. It is
# downloaded at the PINNED version and checked against the release's published
# SHA-256: a floating version changes the CSS nobody asked for, and an
# unverified binary is code run on every developer machine and in CI.
$(TAILWIND_BIN):
	@mkdir -p bin
	@os=$$(uname -s | tr '[:upper:]' '[:lower:]'); \
	arch=$$(uname -m); \
	case "$$os" in darwin) os=macos ;; esac; \
	case "$$arch" in x86_64|amd64) arch=x64 ;; aarch64) arch=arm64 ;; esac; \
	asset="tailwindcss-$$os-$$arch"; \
	case "$$asset" in \
		tailwindcss-linux-x64)   sum=4ab84f2b496c402d3ec4fd25e0e5559fe1184d886dadae8fb4438344ec044c22 ;; \
		tailwindcss-linux-arm64) sum=ad627e77b496cccada4a6e26eafff698ef0829081e575a4baf3af8524bb00747 ;; \
		tailwindcss-macos-arm64) sum=2ce66b7c8101ef1245a07d1e7abb4beb35bf512fd3beecba1cdfb327580d1252 ;; \
		tailwindcss-macos-x64)   sum=98e34c6abd00a75a74ea2d20acf9e284241d13023133076d220c6f3ca419d920 ;; \
		*) echo "no pinned checksum for $$asset" >&2; exit 1 ;; \
	esac; \
	echo "downloading tailwindcss v$(TAILWIND_VERSION) ($$asset)"; \
	curl -fsSL -o $@.tmp "https://github.com/tailwindlabs/tailwindcss/releases/download/v$(TAILWIND_VERSION)/$$asset"; \
	if command -v sha256sum >/dev/null; then got=$$(sha256sum $@.tmp | cut -d' ' -f1); \
	else got=$$(shasum -a 256 $@.tmp | cut -d' ' -f1); fi; \
	if [ "$$got" != "$$sum" ]; then rm -f $@.tmp; echo "checksum mismatch for $$asset" >&2; exit 1; fi; \
	mv $@.tmp $@
	@chmod +x $@

# Checks that the generated files match their sources.
#
# Compares contents before/after regenerating, NOT with `git diff`, so it
# works the same in CI and in a dirty working tree, which is the normal state
# while editing the panel.
ui-check: ## Fail if the panel is not regenerated
	@tmp=$$(mktemp -d); \
	cp internal/api/views/templ/*_templ.go "$$tmp/" 2>/dev/null || true; \
	cp web/static/css/output.css "$$tmp/output.css" 2>/dev/null || true; \
	$(MAKE) --no-print-directory ui >/dev/null; \
	rc=0; \
	for f in internal/api/views/templ/*_templ.go; do \
		cmp -s "$$f" "$$tmp/$$(basename $$f)" || { echo "not regenerated: $$f" >&2; rc=1; }; \
	done; \
	cmp -s web/static/css/output.css "$$tmp/output.css" || \
		{ echo "not regenerated: web/static/css/output.css" >&2; rc=1; }; \
	rm -rf "$$tmp"; \
	test $$rc -eq 0 || { echo "run 'make ui' and commit the result" >&2; exit 1; }

mocks: ## Regenerate tests/mocks with the mockery pinned in go.mod
	@go tool mockery

mocks-check: ## Fail if tests/mocks is not regenerated
	@tmp=$$(mktemp -d); \
	cp tests/mocks/*.go "$$tmp/"; \
	go tool mockery >/dev/null 2>&1; \
	rc=0; \
	for f in tests/mocks/*.go; do \
		cmp -s "$$f" "$$tmp/$$(basename $$f)" || { echo "not regenerated: $$f" >&2; rc=1; }; \
	done; \
	rm -rf "$$tmp"; \
	test $$rc -eq 0 || { echo "run 'make mocks' and commit the result" >&2; exit 1; }

# ---------------------------------------------------------------------------
# Docker
# ---------------------------------------------------------------------------

.PHONY: docker-build docker-run docker-up docker-down docker-monitoring docker-logs
docker-build: ## Build the Docker image
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t $(DOCKER_IMAGE):$(VERSION) .

docker-run: docker-build ## Build and run the image with .env
	docker run -p 8080:8080 --env-file .env $(DOCKER_IMAGE):$(VERSION)

docker-up: ## Start falco with docker compose
	docker compose --profile app up -d --build

docker-monitoring: ## Start falco plus Prometheus and Grafana
	docker compose --profile app --profile monitoring up -d --build

docker-down: ## Stop every compose service
	docker compose --profile app --profile monitoring --profile with-cache --profile with-nginx down

docker-logs: ## Follow the compose logs
	docker compose logs -f

# ---------------------------------------------------------------------------
# Setup and housekeeping
# ---------------------------------------------------------------------------

.PHONY: setup dev-setup clean health help
setup: ## Install development tools (pinned versions)
	go mod download
	go install github.com/air-verse/air@v1.67.4
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

dev-setup: setup ## Full local setup: tools, .env, data directory
	cp -n .env.example .env || true
	mkdir -p data/images logs

clean: ## Remove build artifacts and local images data
	rm -rf bin/ dist/
	rm -rf data/images/*
	rm -f coverage.out coverage.html

health: ## Check a local instance's /health
	curl -fsS http://localhost:8080/health

help: ## List the targets
	@grep -hE '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*## "}; {printf "  %-18s %s\n", $$1, $$2}'
