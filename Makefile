.PHONY: all build run dev dev-frontend test test-go test-web test-coverage smoke clean frontend backend deps generate-key lint fmt help

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
# Go packages of this repository; ./... would also pick up Go files that ship
# inside web/node_modules.
GOPKGS=./cmd/... ./internal/...

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
test: test-go test-web

test-go:
	$(GOTEST) -v $(GOPKGS)

test-web:
	cd $(WEB_DIR) && npm test

# Run tests with coverage
test-coverage:
	$(GOTEST) -v -coverprofile=coverage.out $(GOPKGS)
	$(GOCMD) tool cover -html=coverage.out -o coverage.html

# Run the smoke tests against real servers started by docker compose
smoke:
	$(GOTEST) -v -tags smoke -count=1 ./smoke/...

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
	golangci-lint run $(GOPKGS)
	cd $(WEB_DIR) && npm run lint

# Format code
fmt:
	$(GOCMD) fmt $(GOPKGS)
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
	@echo "  test         - Run Go and frontend tests"
	@echo "  smoke        - Run smoke tests against docker compose servers"
	@echo "  clean        - Clean build artifacts"
	@echo "  lint         - Run linters"
	@echo "  fmt          - Format code"
