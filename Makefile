# `make check` es lo que tiene que pasar antes de un commit. El gate de lint
# (.golangci.yml) es lo que impide que la legibilidad se vuelva a degradar.
.PHONY: check check-fmt check-build
check: check-fmt vet lint test ui-check check-build ## Todo lo que tiene que pasar antes de un commit
	@echo "ok: check completo"

check-fmt: ## Falla si algo está sin formatear
	@test -z "$$(gofmt -l . | grep -v vendor)" || \
		(echo "sin formatear:"; gofmt -l . | grep -v vendor; exit 1)

check-build: ## Compila TODOS los paquetes para el host.
	@# No usa el target `build`: ese cross-compila a Linux con CGO y no corre
	@# en macOS, donde libvips es del host.
	go build ./...

# Makefile de falco — servicio de procesamiento de imágenes

# Variables
BINARY_NAME=falco-server
DOCKER_IMAGE=falco-service
VERSION?=latest

# Build commands
.PHONY: build
build:
	CGO_ENABLED=1 GOOS=linux go build -o bin/$(BINARY_NAME) cmd/server/main.go

.PHONY: build-local
build-local:
	go build -o bin/$(BINARY_NAME) cmd/server/main.go

.PHONY: run
run:
	go run cmd/server/main.go

# air lee .air.toml de la raíz si existe; sin -c no falla cuando no está.
.PHONY: dev
dev:
	air

# Testing commands
.PHONY: test
test:
	go test -v ./...

.PHONY: test-coverage
test-coverage:
	go test -v -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

.PHONY: test-integration
test-integration:
	go test -v -tags=integration ./tests/integration/...

.PHONY: test-performance
test-performance:
	go test -v -bench=. -benchmem ./...

# Code quality commands
.PHONY: lint
lint:
	golangci-lint run

# El lint del host NO es el de CI: hay reglas cuyo resultado depende de la
# plataforma (unconvert sobre syscall.Statfs_t, por ejemplo, donde el tipo del
# campo cambia entre Linux y Darwin). Este target corre el mismo golangci-lint
# que el workflow, dentro de Linux.
.PHONY: lint-linux
lint-linux:
	@scripts/lint-in-container.sh

.PHONY: fmt
fmt:
	go fmt ./...
	goimports -w .

.PHONY: vet
vet:
	go vet ./...

# ---------------------------------------------------------------------------
# Panel web (templ + Tailwind)
#
# Las plantillas y el CSS del panel se COMPILAN. Sin estos targets, editar un
# .templ o una clase de Tailwind no cambia nada de lo que sirve el binario y
# nada lo detecta: los *_templ.go decían v0.3.1001 mientras go.mod exigía
# v0.3.1020, y output.css venía de un tailwind 4.2.2 que no era el de bin/.
# ---------------------------------------------------------------------------

TAILWIND_VERSION := 4.2.2
TAILWIND_BIN     := bin/tailwindcss
TEMPL_SRC        := $(wildcard internal/api/views/templ/*.templ)

.PHONY: ui ui-templ ui-css ui-check
ui: ui-templ ui-css ## Regenera el panel: plantillas templ + CSS de Tailwind

ui-templ: ## Genera los *_templ.go desde los .templ
	@go tool templ generate

ui-css: $(TAILWIND_BIN) ## Compila web/static/css/input.css -> output.css
	@$(TAILWIND_BIN) -i web/static/css/input.css -o web/static/css/output.css

# bin/ está en .gitignore, así que un clone limpio no trae el compilador de
# Tailwind. Se descarga con la versión FIJADA: si flota, el CSS cambia sin que
# nadie lo haya pedido y el diff aparece en un commit ajeno.
$(TAILWIND_BIN):
	@mkdir -p bin
	@os=$$(uname -s | tr '[:upper:]' '[:lower:]'); \
	arch=$$(uname -m); \
	case "$$os" in darwin) os=macos ;; esac; \
	case "$$arch" in x86_64|amd64) arch=x64 ;; aarch64) arch=arm64 ;; esac; \
	url="https://github.com/tailwindlabs/tailwindcss/releases/download/v$(TAILWIND_VERSION)/tailwindcss-$$os-$$arch"; \
	echo "descargando tailwindcss v$(TAILWIND_VERSION) ($$os-$$arch)"; \
	curl -fsSL -o $@ "$$url"
	@chmod +x $@

# Comprueba que los archivos generados corresponden a sus fuentes.
#
# Compara por contenido antes/después de regenerar, NO con `git diff`: así vale
# igual en CI y en un árbol de trabajo sucio, que es el estado normal mientras
# se edita el panel.
ui-check: ## Falla si el panel está sin regenerar
	@tmp=$$(mktemp -d); \
	cp internal/api/views/templ/*_templ.go "$$tmp/" 2>/dev/null || true; \
	cp web/static/css/output.css "$$tmp/output.css" 2>/dev/null || true; \
	$(MAKE) --no-print-directory ui >/dev/null; \
	rc=0; \
	for f in internal/api/views/templ/*_templ.go; do \
		cmp -s "$$f" "$$tmp/$$(basename $$f)" || { echo "sin regenerar: $$f" >&2; rc=1; }; \
	done; \
	cmp -s web/static/css/output.css "$$tmp/output.css" || \
		{ echo "sin regenerar: web/static/css/output.css" >&2; rc=1; }; \
	rm -rf "$$tmp"; \
	test $$rc -eq 0 || { echo "corre 'make ui' y commitea el resultado" >&2; exit 1; }

# Docker commands
.PHONY: docker-build
docker-build:
	docker build -t $(DOCKER_IMAGE):$(VERSION) .

.PHONY: docker-run
docker-run: docker-build
	docker run -p 8080:8080 --env-file .env $(DOCKER_IMAGE):$(VERSION)

.PHONY: docker-compose-up
docker-compose-up:
	docker-compose up --build

.PHONY: docker-compose-down
docker-compose-down:
	docker-compose down

# Monitoring commands
.PHONY: monitoring-up
monitoring-up:
	docker-compose --profile monitoring up -d
	@echo "Prometheus: http://localhost:9090"
	@echo "Grafana: http://localhost:3001 (admin/falco123)"

.PHONY: monitoring-down
monitoring-down:
	docker-compose --profile monitoring down

.PHONY: monitoring-logs
monitoring-logs:
	docker-compose --profile monitoring logs -f

# Docker profile commands
.PHONY: docker-app
docker-app:
	docker-compose --profile app up -d
	@echo "falco: http://localhost:8080"

.PHONY: docker-app-with-monitoring
docker-app-with-monitoring:
	docker-compose --profile app --profile monitoring up -d
	@echo "falco: http://localhost:8080"
	@echo "Prometheus: http://localhost:9090"
	@echo "Grafana: http://localhost:3001 (admin/falco123)"

.PHONY: docker-app-with-cache
docker-app-with-cache:
	docker-compose --profile app --profile with-cache up -d
	@echo "falco: http://localhost:8080"
	@echo "Valkey (Redis): localhost:6379"

.PHONY: docker-app-with-db
docker-app-with-db:
	docker-compose --profile app --profile with-db up -d
	@echo "falco: http://localhost:8080"
	@echo "PostgreSQL: localhost:5432"

.PHONY: docker-app-with-nginx
docker-app-with-nginx:
	docker-compose --profile app --profile with-nginx up -d
	@echo "Nginx: http://localhost:80"
	@echo "falco (backend): http://localhost:8080"

.PHONY: docker-full
docker-full:
	docker-compose --profile app --profile monitoring --profile with-cache --profile with-db --profile with-nginx up -d
	@echo "=== Full Stack Started ==="
	@echo "Nginx: http://localhost:80"
	@echo "falco: http://localhost:8080"
	@echo "Prometheus: http://localhost:9090"
	@echo "Grafana: http://localhost:3001 (admin/falco123)"
	@echo "Valkey (Redis): localhost:6379"
	@echo "PostgreSQL: localhost:5432"

.PHONY: docker-all-down
docker-all-down:
	docker-compose --profile app --profile monitoring --profile with-cache --profile with-db --profile with-nginx down

.PHONY: docker-logs
docker-logs:
	docker-compose logs -f

# Development setup
.PHONY: setup
setup:
	go mod download
	go install github.com/air-verse/air@latest
	go install golang.org/x/tools/cmd/goimports@latest
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest

.PHONY: dev-setup
dev-setup: setup
	cp .env.example .env
	mkdir -p data/images
	mkdir -p logs

# Cleanup commands
.PHONY: clean
clean:
	rm -rf bin/
	rm -rf data/images/*
	rm -f coverage.out coverage.html
	docker system prune -f

# Deployment commands
.PHONY: deploy-staging
deploy-staging:
	@echo "Deploying to staging environment..."
	# Add staging deployment commands here

.PHONY: deploy-production
deploy-production:
	@echo "Deploying to production environment..."
	# Add production deployment commands here

# Health check
.PHONY: health
health:
	curl -f http://localhost:8080/health || exit 1

# Load test for metrics
.PHONY: load-test
load-test:
	@chmod +x scripts/load-test.sh
	@./scripts/load-test.sh

# Help
.PHONY: help
help:
	@echo "Available commands:"
	@echo ""
	@echo "  Build:"
	@echo "    build              Build the binary for Linux"
	@echo "    build-local        Build the binary for local OS"
	@echo ""
	@echo "  Run:"
	@echo "    run                Run the application locally"
	@echo "    dev                Run with hot reload (requires air)"
	@echo ""
	@echo "  Test:"
	@echo "    test               Run all tests"
	@echo "    test-coverage      Run tests with coverage report"
	@echo "    test-integration   Run integration tests"
	@echo "    test-performance   Run performance benchmarks"
	@echo ""
	@echo "  Panel web:"
	@echo "    ui                 Regenera plantillas templ + CSS de Tailwind"
	@echo "    ui-check           Falla si el panel está sin regenerar"
	@echo ""
	@echo "  Code Quality:"
	@echo "    lint               Run linter"
	@echo "    fmt                Format code"
	@echo "    vet                Run go vet"
	@echo ""
	@echo "  Docker:"
	@echo "    docker-build       Build Docker image"
	@echo "    docker-run         Build and run Docker container"
	@echo "    docker-compose-up  Run with docker-compose (default)"
	@echo "    docker-app         Run falco-service in Docker"
	@echo "    docker-full        Run ALL services (app, monitoring, cache, db, nginx)"
	@echo "    docker-all-down    Stop ALL Docker services"
	@echo "    docker-logs        View all Docker logs"
	@echo ""
	@echo "  Docker Profiles:"
	@echo "    docker-app-with-monitoring   App + Prometheus + Grafana"
	@echo "    docker-app-with-cache        App + Valkey (Redis)"
	@echo "    docker-app-with-db           App + PostgreSQL"
	@echo "    docker-app-with-nginx        App + Nginx reverse proxy"
	@echo ""
	@echo "  Monitoring:"
	@echo "    monitoring-up      Start Prometheus + Grafana (for local dev)"
	@echo "    monitoring-down    Stop monitoring stack"
	@echo "    monitoring-logs    View monitoring logs"
	@echo ""
	@echo "  Setup:"
	@echo "    setup              Install development dependencies"
	@echo "    dev-setup          Complete development environment setup"
	@echo "    clean              Clean build artifacts and data"
	@echo ""
	@echo "  Other:"
	@echo "    health             Check service health"
	@echo "    help               Show this help message"