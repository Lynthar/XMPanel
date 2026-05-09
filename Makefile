.PHONY: all build run dev test clean frontend backend deps

# Variables
BINARY_NAME=xmpanel
MAIN_PATH=./cmd/server
WEB_DIR=web

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOTEST=$(GOCMD) test
GOMOD=$(GOCMD) mod
GORUN=$(GOCMD) run

all: deps build

# Download dependencies
deps:
	$(GOMOD) tidy
	cd $(WEB_DIR) && npm install

# Build everything
build: backend frontend

# Build backend
backend:
	CGO_ENABLED=0 $(GOBUILD) -o $(BINARY_NAME) $(MAIN_PATH)

# Build frontend
frontend:
	cd $(WEB_DIR) && npm run build

# Run backend in development mode
run:
	$(GORUN) $(MAIN_PATH)

# Run frontend dev server
dev-frontend:
	cd $(WEB_DIR) && npm run dev

# Run both backend and frontend in development
dev:
	@echo "Starting backend..."
	$(GORUN) $(MAIN_PATH) &
	@echo "Starting frontend dev server..."
	cd $(WEB_DIR) && npm run dev

# Run tests
test:
	$(GOTEST) -v ./...

# Run tests with coverage
test-coverage:
	$(GOTEST) -v -coverprofile=coverage.out ./...
	$(GOCMD) tool cover -html=coverage.out -o coverage.html

# Clean build artifacts
clean:
	rm -f $(BINARY_NAME)
	rm -f coverage.out coverage.html
	rm -rf $(WEB_DIR)/dist
	rm -rf $(WEB_DIR)/node_modules

# Generate encryption key (base64 32-byte for database.encryption_key)
generate-key:
	@head -c 32 /dev/urandom | base64

# Lint
lint:
	golangci-lint run ./...
	cd $(WEB_DIR) && npm run lint

# Format code
fmt:
	$(GOCMD) fmt ./...
	cd $(WEB_DIR) && npm run format

# Help
help:
	@echo "Available targets:"
	@echo "  all          - Download dependencies and build"
	@echo "  deps         - Download Go and npm dependencies"
	@echo "  build        - Build backend and frontend"
	@echo "  backend      - Build backend only"
	@echo "  frontend     - Build frontend only"
	@echo "  run          - Run backend"
	@echo "  dev-frontend - Run frontend dev server"
	@echo "  dev          - Run both in development mode"
	@echo "  test         - Run tests"
	@echo "  clean        - Clean build artifacts"
	@echo "  lint         - Run linters"
	@echo "  fmt          - Format code"
