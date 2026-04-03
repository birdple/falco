#!/bin/bash

# Build script for Imagine Image Processing Service
set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Configuration
IMAGE_NAME="falco-service"
TAG=${TAG:-latest}
DOCKERFILE=${DOCKERFILE:-Dockerfile}
BUILD_CONTEXT=${BUILD_CONTEXT:-.}

# Functions
log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

log_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Check if Docker is installed
check_docker() {
    if ! command -v docker &> /dev/null; then
        log_error "Docker is not installed or not in PATH"
        exit 1
    fi

    if ! docker info &> /dev/null; then
        log_error "Docker daemon is not running"
        exit 1
    fi
}

# Build Docker image
build_image() {
    log_info "Building Docker image: ${IMAGE_NAME}:${TAG}"

    # Build arguments
    BUILD_ARGS=""
    if [ -n "$BUILDKIT_INLINE_CACHE" ]; then
        BUILD_ARGS="$BUILD_ARGS --build-arg BUILDKIT_INLINE_CACHE=1"
    fi

    # Build the image
    docker build \
        --file ${DOCKERFILE} \
        --tag ${IMAGE_NAME}:${TAG} \
        --target production \
        ${BUILD_ARGS} \
        ${BUILD_CONTEXT}

    log_success "Docker image built successfully: ${IMAGE_NAME}:${TAG}"
}

# Run tests before building
run_tests() {
    if [ "$SKIP_TESTS" != "true" ]; then
        log_info "Running tests..."

        if ! go test -v ./...; then
            log_error "Tests failed. Aborting build."
            exit 1
        fi

        log_success "All tests passed"
    else
        log_warning "Skipping tests"
    fi
}

# Lint code
run_lint() {
    if [ "$SKIP_LINT" != "true" ]; then
        log_info "Running linter..."

        if command -v golangci-lint &> /dev/null; then
            if ! golangci-lint run; then
                log_error "Linting failed. Aborting build."
                exit 1
            fi
            log_success "Linting passed"
        else
            log_warning "golangci-lint not found, skipping linting"
        fi
    else
        log_warning "Skipping linting"
    fi
}

# Show build information
show_info() {
    log_info "Build Information:"
    echo "  Image Name: ${IMAGE_NAME}"
    echo "  Tag: ${TAG}"
    echo "  Dockerfile: ${DOCKERFILE}"
    echo "  Build Context: ${BUILD_CONTEXT}"
    echo "  Skip Tests: ${SKIP_TESTS:-false}"
    echo "  Skip Lint: ${SKIP_LINT:-false}"
}

# Main build process
main() {
    log_info "Starting build process for Imagine Image Processing Service"

    show_info
    echo

    check_docker
    run_lint
    run_tests
    build_image

    log_success "Build completed successfully!"
    echo
    log_info "To run the container:"
    echo "  docker run -p 8080:8080 ${IMAGE_NAME}:${TAG}"
    echo
    log_info "To use with docker-compose:"
    echo "  docker-compose up --build"
}

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --tag=*)
            TAG="${1#*=}"
            shift
            ;;
        --image=*)
            IMAGE_NAME="${1#*=}"
            shift
            ;;
        --dockerfile=*)
            DOCKERFILE="${1#*=}"
            shift
            ;;
        --context=*)
            BUILD_CONTEXT="${1#*=}"
            shift
            ;;
        --skip-tests)
            SKIP_TESTS=true
            shift
            ;;
        --skip-lint)
            SKIP_LINT=true
            shift
            ;;
        --help)
            echo "Usage: $0 [OPTIONS]"
            echo
            echo "Options:"
            echo "  --tag=TAG           Docker image tag (default: latest)"
            echo "  --image=NAME        Docker image name (default: falco-service)"
            echo "  --dockerfile=FILE   Dockerfile path (default: Dockerfile)"
            echo "  --context=PATH      Build context path (default: .)"
            echo "  --skip-tests        Skip running tests"
            echo "  --skip-lint         Skip running linter"
            echo "  --help              Show this help message"
            exit 0
            ;;
        *)
            log_error "Unknown option: $1"
            echo "Use --help for usage information"
            exit 1
            ;;
    esac
done

# Run main function
main