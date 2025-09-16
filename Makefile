# Makefile for Imagine Image Processing Service

# Variables
BINARY_NAME=imagine-server
DOCKER_IMAGE=imagine-service
VERSION?=latest
GO_VERSION=1.21

# Build commands
.PHONY: build
build:
	CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o bin/$(BINARY_NAME) cmd/server/main.go

.PHONY: build-local
build-local:
	go build -o bin/$(BINARY_NAME) cmd/server/main.go

.PHONY: run
run:
	go run cmd/server/main.go

.PHONY: dev
dev:
	air -c .air.toml

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

# Development setup
.PHONY: setup
setup:
	go mod download
	go install github.com/cosmtrek/air@latest
	go install golang.org/x/tools/cmd/goimports@latest
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

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

# Help
.PHONY: help
help:
	@echo "Available commands:"
	@echo "  build              Build the binary for Linux"
	@echo "  build-local        Build the binary for local OS"
	@echo "  run                Run the application locally"
	@echo "  dev                Run with hot reload (requires air)"
	@echo "  test               Run all tests"
	@echo "  test-coverage      Run tests with coverage report"
	@echo "  test-integration   Run integration tests"
	@echo "  test-performance   Run performance benchmarks"
	@echo "  lint               Run linter"
	@echo "  fmt                Format code"
	@echo "  vet                Run go vet"
	@echo "  docker-build       Build Docker image"
	@echo "  docker-run         Build and run Docker container"
	@echo "  docker-compose-up  Run with docker-compose"
	@echo "  setup              Install development dependencies"
	@echo "  dev-setup          Complete development environment setup"
	@echo "  clean              Clean build artifacts and data"
	@echo "  health             Check service health"
	@echo "  help               Show this help message"