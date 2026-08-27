# `make check` es lo que tiene que pasar antes de un commit. El gate de lint
# (.golangci.yml) es lo que impide que la legibilidad se vuelva a degradar.
.PHONY: check check-fmt check-build
check: check-fmt vet lint test check-build ## Todo lo que tiene que pasar antes de un commit
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

.PHONY: fmt
fmt:
	go fmt ./...
	goimports -w .

.PHONY: vet
vet:
	go vet ./...

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
	@echo "Imagine Service: http://localhost:8080"

.PHONY: docker-app-with-monitoring
docker-app-with-monitoring:
	docker-compose --profile app --profile monitoring up -d
	@echo "Imagine Service: http://localhost:8080"
	@echo "Prometheus: http://localhost:9090"
	@echo "Grafana: http://localhost:3001 (admin/falco123)"

.PHONY: docker-app-with-cache
docker-app-with-cache:
	docker-compose --profile app --profile with-cache up -d
	@echo "Imagine Service: http://localhost:8080"
	@echo "Valkey (Redis): localhost:6379"

.PHONY: docker-app-with-db
docker-app-with-db:
	docker-compose --profile app --profile with-db up -d
	@echo "Imagine Service: http://localhost:8080"
	@echo "PostgreSQL: localhost:5432"

.PHONY: docker-app-with-nginx
docker-app-with-nginx:
	docker-compose --profile app --profile with-nginx up -d
	@echo "Nginx: http://localhost:80"
	@echo "Imagine Service (backend): http://localhost:8080"

.PHONY: docker-full
docker-full:
	docker-compose --profile app --profile monitoring --profile with-cache --profile with-db --profile with-nginx up -d
	@echo "=== Full Stack Started ==="
	@echo "Nginx: http://localhost:80"
	@echo "Imagine Service: http://localhost:8080"
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