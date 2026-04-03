#!/bin/bash

# Deployment script for Imagine Image Processing Service
set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Configuration
ENVIRONMENT=${ENVIRONMENT:-production}
IMAGE_NAME="falco-service"
TAG=${TAG:-latest}
DOCKER_COMPOSE_FILE="docker-compose.yml"

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

# Check prerequisites
check_prerequisites() {
    log_info "Checking prerequisites..."

    # Check if Docker is installed
    if ! command -v docker &> /dev/null; then
        log_error "Docker is not installed"
        exit 1
    fi

    # Check if Docker Compose is installed
    if ! command -v docker-compose &> /dev/null && ! docker compose version &> /dev/null; then
        log_error "Docker Compose is not installed"
        exit 1
    fi

    # Check if .env file exists
    if [ ! -f ".env" ]; then
        log_warning ".env file not found. Creating from template..."
        if [ -f ".env.example" ]; then
            cp .env.example .env
            log_warning "Please edit .env file with your configuration"
        else
            log_error ".env.example file not found"
            exit 1
        fi
    fi

    log_success "Prerequisites check passed"
}

# Build the application
build_app() {
    if [ "$SKIP_BUILD" != "true" ]; then
        log_info "Building application..."

        if [ -f "scripts/build.sh" ]; then
            ./scripts/build.sh --tag=${TAG}
        else
            log_warning "Build script not found, building with docker-compose..."
            docker-compose -f ${DOCKER_COMPOSE_FILE} build --no-cache
        fi

        log_success "Application built successfully"
    else
        log_warning "Skipping build"
    fi
}

# Deploy to environment
deploy_app() {
    log_info "Deploying to ${ENVIRONMENT} environment..."

    case ${ENVIRONMENT} in
        "development"|"dev")
            DOCKER_COMPOSE_FILE="docker-compose.yml"
            ;;
        "production"|"prod")
            DOCKER_COMPOSE_FILE="docker-compose.prod.yml"
            ;;
        "staging"|"stage")
            DOCKER_COMPOSE_FILE="docker-compose.staging.yml"
            ;;
        *)
            log_error "Unknown environment: ${ENVIRONMENT}"
            echo "Supported environments: development, production, staging"
            exit 1
            ;;
    esac

    # Check if compose file exists
    if [ ! -f "${DOCKER_COMPOSE_FILE}" ]; then
        log_error "Docker Compose file not found: ${DOCKER_COMPOSE_FILE}"
        exit 1
    fi

    # Stop existing containers
    log_info "Stopping existing containers..."
    docker-compose -f ${DOCKER_COMPOSE_FILE} down || true

    # Start new containers
    log_info "Starting containers..."
    docker-compose -f ${DOCKER_COMPOSE_FILE} up -d

    # Wait for services to be healthy
    log_info "Waiting for services to be healthy..."
    sleep 10

    # Check health
    if check_health; then
        log_success "Deployment completed successfully!"
        show_service_info
    else
        log_error "Deployment failed - services are not healthy"
        show_logs
        exit 1
    fi
}

# Check service health
check_health() {
    local max_attempts=30
    local attempt=1

    log_info "Checking service health..."

    while [ $attempt -le $max_attempts ]; do
        if curl -f -s http://localhost:8080/health > /dev/null 2>&1; then
            log_success "Service is healthy"
            return 0
        fi

        log_info "Waiting for service to be healthy (attempt $attempt/$max_attempts)..."
        sleep 5
        ((attempt++))
    done

    log_error "Service failed to become healthy"
    return 1
}

# Show service information
show_service_info() {
    echo
    log_info "Service Information:"
    echo "  Environment: ${ENVIRONMENT}"
    echo "  Health Check: http://localhost:8080/health"
    echo "  API Documentation: http://localhost:8080/api/v1/"
    echo
    log_info "Container Status:"
    docker-compose -f ${DOCKER_COMPOSE_FILE} ps
}

# Show logs
show_logs() {
    echo
    log_warning "Recent logs:"
    docker-compose -f ${DOCKER_COMPOSE_FILE} logs --tail=50
}

# Rollback deployment
rollback() {
    log_warning "Rolling back deployment..."

    # Stop current deployment
    docker-compose -f ${DOCKER_COMPOSE_FILE} down

    # Start previous version (if available)
    if [ -n "$ROLLBACK_TAG" ]; then
        log_info "Starting rollback to tag: ${ROLLBACK_TAG}"
        TAG=${ROLLBACK_TAG} deploy_app
    else
        log_error "No rollback tag specified"
        exit 1
    fi
}

# Cleanup old images and containers
cleanup() {
    log_info "Cleaning up old Docker resources..."

    # Remove unused containers
    docker container prune -f

    # Remove unused images
    docker image prune -f

    # Remove unused volumes
    docker volume prune -f

    log_success "Cleanup completed"
}

# Show usage information
show_usage() {
    echo "Usage: $0 [OPTIONS] COMMAND"
    echo
    echo "Commands:"
    echo "  deploy     Deploy the application"
    echo "  rollback   Rollback to previous version"
    echo "  cleanup    Clean up old Docker resources"
    echo "  status     Show deployment status"
    echo
    echo "Options:"
    echo "  --environment=ENV   Target environment (development, production, staging)"
    echo "  --tag=TAG           Docker image tag"
    echo "  --skip-build        Skip building the application"
    echo "  --rollback-tag=TAG  Tag to rollback to"
    echo "  --help              Show this help message"
    echo
    echo "Examples:"
    echo "  $0 deploy"
    echo "  $0 --environment=production deploy"
    echo "  $0 --tag=v1.2.3 deploy"
    echo "  $0 rollback --rollback-tag=v1.1.0"
    echo "  $0 cleanup"
}

# Show deployment status
show_status() {
    log_info "Deployment Status:"

    if [ -f "${DOCKER_COMPOSE_FILE}" ]; then
        echo "  Docker Compose File: ${DOCKER_COMPOSE_FILE}"
        echo
        docker-compose -f ${DOCKER_COMPOSE_FILE} ps
        echo
        log_info "Service Health:"
        if curl -f -s http://localhost:8080/health > /dev/null 2>&1; then
            echo "  ✅ Service is healthy"
        else
            echo "  ❌ Service is not healthy"
        fi
    else
        log_warning "No deployment found"
    fi
}

# Main function
main() {
    local command=${1:-deploy}

    case ${command} in
        "deploy")
            check_prerequisites
            build_app
            deploy_app
            ;;
        "rollback")
            rollback
            ;;
        "cleanup")
            cleanup
            ;;
        "status")
            show_status
            ;;
        "--help"|"-h")
            show_usage
            exit 0
            ;;
        *)
            log_error "Unknown command: ${command}"
            echo
            show_usage
            exit 1
            ;;
    esac
}

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --environment=*)
            ENVIRONMENT="${1#*=}"
            shift
            ;;
        --tag=*)
            TAG="${1#*=}"
            shift
            ;;
        --rollback-tag=*)
            ROLLBACK_TAG="${1#*=}"
            shift
            ;;
        --skip-build)
            SKIP_BUILD=true
            shift
            ;;
        --help)
            show_usage
            exit 0
            ;;
        *)
            break
            ;;
    esac
done

# Run main function
main "$@"