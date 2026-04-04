# SwarmCracker Makefile

# Build variables
BINARY_NAME=swarmcracker
VERSION?=$(shell git describe --tags --always --dirty 2>/dev/null || echo "v0.1.0-alpha")
BUILD_TIME?=$(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
GIT_COMMIT?=$(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
LDFLAGS=-ldflags "-X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME) -X main.GitCommit=$(GIT_COMMIT)"
GOFLAGS=-v
GO=go

# Directories
CMD_DIR=./cmd
PKG_DIR=./pkg
BUILD_DIR=./build
DIST_DIR=./dist

# binaries
swarmcracker:
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p $(BUILD_DIR)
	$(GO) build $(GOFLAGS) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) $(CMD_DIR)/swarmcracker

swarmd-firecracker:
	@echo "Building swarmd-firecracker..."
	@mkdir -p $(BUILD_DIR)
	$(GO) build $(GOFLAGS) $(LDFLAGS) -o $(BUILD_DIR)/swarmd-firecracker $(CMD_DIR)/swarmd-firecracker/main.go

swarmcracker-agent:
	@echo "Building swarmcracker-agent..."
	@mkdir -p $(BUILD_DIR)
	$(GO) build $(GOFLAGS) $(LDFLAGS) -o $(BUILD_DIR)/swarmcracker-agent $(CMD_DIR)/swarmcracker-agent/main.go

# Build all binaries
all: swarmcracker swarmd-firecracker swarmcracker-agent

# Install binaries to $GOPATH/bin
install: all
	@echo "Installing $(BINARY_NAME)..."
	@mkdir -p $$GOPATH/bin
	@cp $(BUILD_DIR)/$(BINARY_NAME) $$GOPATH/bin/

# Run tests
test:
	@echo "Running tests..."
	$(GO) test -v -race -coverprofile=coverage.out ./pkg/...
	$(GO) tool cover -html=coverage.out -o coverage.html

# Run unit tests only
test-unit:
	@echo "Running unit tests..."
	$(GO) test -short -v ./pkg/...

# Run integration tests
test-integration:
	@echo "Running integration tests..."
	$(GO) test -v -tags=integration ./pkg/...

# Run E2E tests
test-e2e:
	@echo "Running E2E tests..."
	@chmod +x test/e2e/run.sh
	@./test/e2e/run.sh

# Run E2E tests with benchmarks
test-e2e-bench:
	@echo "Running E2E tests with benchmarks..."
	@chmod +x test/e2e/run.sh
	@./test/e2e/run.sh --benchmarks

# Run linting
lint:
	@echo "Running linters..."
	golangci-lint run ./...
	@if command -v staticcheck >/dev/null 2>&1; then \
		staticcheck ./...; \
	fi

# Format code
fmt:
	@echo "Formatting code..."
	$(GO) fmt ./...
	goimports -w .

# Clean build artifacts
clean:
	@echo "Cleaning..."
	rm -rf $(BUILD_DIR)
	rm -rf $(DIST_DIR)
	rm -f coverage.out coverage.html

# Run examples
examples: all
	@echo "Running examples..."
	$(BUILD_DIR)/$(BINARY_NAME) --help

# Build release binaries for multiple platforms
release:
	@echo "Building release binaries for $(VERSION)..."
	@mkdir -p $(DIST_DIR)
	@for os in linux darwin; do \
		for arch in amd64 arm64; do \
			echo "Building $$os/$$arch..."; \
			for bin in swarmcracker swarmd-firecracker swarmcracker-agent; do \
				CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build $(LDFLAGS) \
					-o $(DIST_DIR)/$$bin-$$os-$$arch ./cmd/$$bin; \
			done; \
			pkg_dir=$(DIST_DIR)/swarmcracker-$(VERSION)-$$os-$$arch; \
			mkdir -p $$pkg_dir; \
			cp $(DIST_DIR)/swarmcracker-$$os-$$arch $$pkg_dir/swarmcracker; \
			cp $(DIST_DIR)/swarmd-firecracker-$$os-$$arch $$pkg_dir/swarmd-firecracker; \
			cp $(DIST_DIR)/swarmcracker-agent-$$os-$$arch $$pkg_dir/swarmcracker-agent; \
			cp LICENSE README.md $$pkg_dir/; \
			tar -czf $(DIST_DIR)/swarmcracker-$(VERSION)-$$os-$$arch.tar.gz \
				-C $(DIST_DIR) swarmcracker-$(VERSION)-$$os-$$arch; \
			rm -rf $$pkg_dir; \
		done; \
	done
	@echo "Cleaning intermediate binaries..."
	@rm -f $(DIST_DIR)/swarmcracker-linux-* $(DIST_DIR)/swarmcracker-darwin-*
	@rm -f $(DIST_DIR)/swarmd-firecracker-linux-* $(DIST_DIR)/swarmd-firecracker-darwin-*
	@rm -f $(DIST_DIR)/swarmcracker-agent-linux-* $(DIST_DIR)/swarmcracker-agent-darwin-*
	@echo "Generating checksums..."
	@cd $(DIST_DIR) && sha256sum *.tar.gz > checksums.txt
	@echo ""
	@echo "Release artifacts:"
	@ls -lh $(DIST_DIR)/*.tar.gz $(DIST_DIR)/checksums.txt

# Generate documentation
docs:
	@echo "Generating documentation..."
	@mkdir -p docs/api
	godoc -html github.com/restuhaqza/swarmcracker/pkg/executor > docs/api/executor.html

# Run with race detector
race:
	@echo "Running with race detector..."
	$(GO) test -race ./pkg/...

# Download dependencies
deps:
	@echo "Downloading dependencies..."
	$(GO) mod download
	$(GO) mod tidy

# Run integration tests
integration-test:
	@echo "Running integration tests..."
	$(GO) test -v -tags=integration ./test/integration/...

# Run E2E tests
e2e-test:
	@echo "Running E2E tests..."
	$(GO) test -v -timeout=30m ./test/e2e/...

# Run testinfra tests
testinfra:
	@echo "Running testinfra checks..."
	$(GO) test -v ./test/testinfra/...

# Run all tests (unit, integration, e2e)
test-all: test integration-test testinfra
	@echo "All test suites completed"

# Quick tests (unit only, skip E2E)
test-quick:
	@echo "Running quick tests (unit only)..."
	$(GO) test -short -v ./pkg/...

# Create docker image
docker-image:
	@echo "Building Docker image..."
	docker build -t swarmcracker:$(VERSION) .

# Run with hot reload (development)
dev:
	@echo "Starting development server..."
	air

# Install development tools
install-tools:
	@echo "Installing development tools..."
	$(GO) install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	$(GO) install golang.org/x/tools/cmd/goimports@latest
	$(GO) install github.com/air-verse/air@latest
	$(GO) install github.com/golang/mock/mockgen@latest

# Generate mocks
mocks:
	@echo "Generating mocks..."
	mockgen -source=pkg/executor/executor.go -destination=test/mocks/executor_mock.go

# Show help
help:
	@echo "SwarmCracker Makefile"
	@echo ""
	@echo "Usage:"
	@echo "  make [target]"
	@echo ""
	@echo "Targets:"
	@echo "  all              Build all binaries"
	@echo "  swarmcracker     Build main binary"
	@echo "  install          Install binaries to \$GOPATH/bin"
	@echo "  test             Run unit tests"
	@echo "  test-quick       Run quick tests (unit only, skip E2E)"
	@echo "  test-all         Run all tests (unit, integration, E2E, testinfra)"
	@echo "  integration-test Run integration tests"
	@echo "  e2e-test         Run end-to-end tests"
	@echo "  testinfra        Run infrastructure tests"
	@echo "  lint             Run linters"
	@echo "  fmt              Format code"
	@echo "  clean            Clean build artifacts"
	@echo "  examples         Run examples"
	@echo "  release          Build release binaries"
	@echo "  docs             Generate documentation"
	@echo "  race             Run with race detector"
	@echo "  deps             Download dependencies"
	@echo "  docker-image     Build Docker image"
	@echo "  dev              Start development server with hot reload"
	@echo "  install-tools    Install development tools"
	@echo "  mocks            Generate mocks"
	@echo "  vagrant-up       Start Vagrant environment"
	@echo "  vagrant-halt     Stop Vagrant environment"
	@echo "  vagrant-destroy  Destroy Vagrant environment"
	@echo "  help             Show this help message"

# Create and push a release tag
tag:
	@if [ -z "$(TAG)" ]; then echo "Usage: make tag TAG=v0.1.0"; exit 1; fi
	@echo "Creating tag $(TAG)..."
	@git tag -a $(TAG) -m "Release $(TAG)"
	@echo "Tag $(TAG) created. Push with:"
	@echo "  git push origin $(TAG)"

.PHONY: all swarmcracker install test test-quick test-all integration-test e2e-test testinfra lint fmt clean examples release tag docs race deps docker-image dev install-tools mocks help vagrant-up vagrant-halt vagrant-destroy

# Vagrant helpers
vagrant-up:
	@echo "Starting Vagrant environment..."
	cd test-automation && vagrant up

vagrant-halt:
	@echo "Stopping Vagrant environment..."
	cd test-automation && vagrant halt

vagrant-destroy:
	@echo "Destroying Vagrant environment..."
	cd test-automation && vagrant destroy -f
