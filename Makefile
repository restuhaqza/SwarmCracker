# SwarmCracker Makefile

# Build variables
BINARY_NAME=swarmcracker
VERSION?=$(shell git describe --tags --always --dirty 2>/dev/null || echo "v0.1.0-alpha")
BUILD_TIME?=$(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
GIT_COMMIT?=$(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
LDFLAGS=-ldflags "-s -w -X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME) -X main.GitCommit=$(GIT_COMMIT)"
GOFLAGS=-v -trimpath -buildmode=pie
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
	$(GO) build $(GOFLAGS) $(LDFLAGS) -o $(BUILD_DIR)/swarmd-firecracker $(CMD_DIR)/swarmd-firecracker

swarmcracker-agent:
	@echo "Building swarmcracker-agent..."
	@mkdir -p $(BUILD_DIR)
	$(GO) build $(GOFLAGS) $(LDFLAGS) -o $(BUILD_DIR)/swarmcracker-agent $(CMD_DIR)/swarmcracker-agent

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

# API contract acceptance: envelopes, OpenAPI fixtures, exit codes, the phase-1
# end-to-end suite (test/api) and the command-level contract paths. Only the cmd
# package is Linux-only; everything before the split runs on every platform.
api-check:
	@echo "Running API contract acceptance..."
	$(GO) test ./pkg/api/v1 ./pkg/client ./pkg/output ./pkg/apihttp ./test/api
	@if [ "$$(uname -s)" = "Linux" ]; then \
		$(GO) test ./cmd/swarmcracker -run 'Test.*Contract|TestCLIExitCode|TestExitCode|Test.*ControlClientBoundary' -count=1; \
	else \
		echo "Skipping cmd/swarmcracker contract tests on $$(uname -s); run make api-check on Linux/CI"; \
	fi

# API conformance fixtures. `conformance` runs the same declarative cases an SDK
# can be pointed at against the real apihttp handler with an in-memory backend on
# loopback TLS: no cluster, no root, no Firecracker. `conformance-lab` runs the
# identical fixtures against a live manager; it skips cleanly when
# CONFORMANCE_HOST/CONFORMANCE_READER_TOKEN are not set, refuses a
# non-https:// host (the run carries bearer tokens), and mutates nothing unless
# CONFORMANCE_ALLOW_MUTATION=1. Once it starts it is strict: a case that did not
# run fails it unless CONFORMANCE_ALLOW_SKIPS=1. See test/conformance/README.md.
conformance:
	$(GO) test ./test/conformance -run TestConformanceMock -count=1

conformance-lab:
	$(GO) test ./test/conformance -run TestConformanceLab -count=1 -timeout 10m

# SDK models and clients. `sdk-gen` rewrites the committed generated files from
# the normative spec, `sdk-check` proves they are current without writing (and
# that the Python package compiles), and `sdk-test` runs both stdlib-only client
# suites. No network and no third-party package is needed for any of them.
sdk-gen:
	$(GO) run ./tools/sdkgen -spec api/openapi.yaml

sdk-check:
	$(GO) run ./tools/sdkgen -spec api/openapi.yaml -check
	python3 -m compileall -q sdk/python/swarmcracker

sdk-test-python:
	python3 -m unittest discover -s sdk/python/tests -t sdk/python

# The Node test runner in the Node 22 build we use does not accept a directory
# argument, so the shell expands the file list here. That works on Node 18+.
sdk-test-node:
	node --test sdk/node/test/*.test.mjs

sdk-test: sdk-test-python sdk-test-node

# Type-check the Node declarations with the pinned TypeScript compiler, the only
# devDependency of sdk/node. `npm ci` must run first (CI installs from the
# committed lockfile in its own step); the runtime package keeps zero
# dependencies, so `npm ci --omit=dev` installs nothing and the client stays
# importable with the Node standard library alone. The second step regenerates
# tools/sdkgen/testdata/accepted.yaml into a temp dir and type-checks it, and
# re-proves that every name the generator refuses as a TypeScript declaration is
# refused by this compiler too. That Go step skips with an actionable message
# when node_modules is absent, so `go test ./tools/sdkgen` and `make sdk-check`
# still run on a bare checkout; this target itself needs `npm ci` first, and the
# sdk CI job installs before calling it, so the gates never skip there.
sdk-typecheck:
	cd sdk/node && npm run typecheck
	$(GO) test -v ./tools/sdkgen -run 'TestPinnedTSC|TestAcceptedFixtureTypeChecks' -count=1

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
	rm -f *.out *.test
	rm -f swarmcracker swarmcracker-agent swarmctl swarmd-firecracker
	rm -rf docs-site/dist docs-site/.astro

# Runnable SDK examples. `make examples` builds the loopback fixture in
# test/examples/mockserver and runs the Python and Node examples against it. It
# needs go, python3 and node: no cluster, no root, no Docker, no network.
examples:
	@./test/examples/run.sh

# CLI-only image: the client, not the daemon (that is Dockerfile). Non-root, no
# shell; mount the CA and token read-only. See Dockerfile.cli for the run line.
cli-image:
	docker build -f Dockerfile.cli -t swarmcracker-cli:dev .

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
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_TIME=$(BUILD_TIME) \
		-t swarmcracker:$(VERSION) .

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
	@echo "  examples         Run the SDK examples against a loopback fixture"
	@echo "  cli-image        Build the client-only Docker image (Dockerfile.cli)"
	@echo "  release          Build release binaries"
	@echo "  docs             Generate documentation"
	@echo "  race             Run with race detector"
	@echo "  deps             Download dependencies"
	@echo "  docker-image     Build Docker image"
	@echo "  dev              Start development server with hot reload"
	@echo "  install-tools    Install development tools"
	@echo "  mocks            Generate mocks"
	@echo "  lab              Create a multi-node dev lab (nested VMs)"
	@echo "  lab-down         Destroy the multi-node dev lab"
	@echo "  check-agent-binary  Fail unless the committed guest agent is the build of these sources"
	@echo "  help             Show this help message"

# Create and push a release tag
tag:
	@if [ -z "$(TAG)" ]; then echo "Usage: make tag TAG=v0.1.0"; exit 1; fi
	@echo "Creating tag $(TAG)..."
	@git tag -a $(TAG) -m "Release $(TAG)"
	@echo "Tag $(TAG) created. Push with:"
	@echo "  git push origin $(TAG)"

.PHONY: all swarmcracker install test test-quick test-all api-check sdk-gen sdk-check sdk-test sdk-test-python sdk-test-node sdk-typecheck integration-test e2e-test testinfra lint fmt clean examples cli-image release tag docs race deps docker-image dev install-tools mocks lab lab-down help binaries

# Download embedded static binaries for VM rootfs injection
.PHONY: binaries
binaries: pkg/image/binaries/tini-amd64 pkg/image/binaries/busybox-amd64 pkg/image/binaries/sc-agent-amd64

pkg/image/binaries/tini-amd64:
	@echo "Downloading tini-static-amd64..."
	@mkdir -p pkg/image/binaries
	curl -L -o $@ https://github.com/krallin/tini/releases/download/v0.19.0/tini-static-amd64
	chmod +x $@

pkg/image/binaries/busybox-amd64:
	@echo "Downloading busybox-x86_64..."
	@mkdir -p pkg/image/binaries
	curl -L -o $@ https://busybox.net/downloads/binaries/1.35.0-x86_64-linux-musl/busybox
	chmod +x $@

# The guest agent is compiled from these sources; declaring them makes rebuilds
# track the code so the committed artifact cannot drift silently.
#
# Test files are left out on purpose. They are not compiled into the agent, so a
# test-only edit must not change the agent's identity below: including them would
# make this target rewrite a 6.9 MB blob — and `make check-agent-binary` demand a
# new one — for a change that cannot affect a single byte of the binary.
#
# The list is sorted because the identity must be a function of the sources and of
# nothing else. $(wildcard) hands back the names its glob produced, and glibc's
# glob sorts them by the *locale's* collation rather than by bytes: under
# en_US.UTF-8 "files.go" comes before "file_uid_other.go", under the C locale it
# comes after. A CI runner runs in the C locale and a developer's shell usually
# does not, so without this $(sort) — which compares bytes — one tree computed two
# identities and `make check-agent-binary` failed on a blob that was a faithful
# build of its sources (#197: 16e016f4c09bf839 here, 9e358e9fae5096e7 on the
# runner, for the same 27 files).
AGENT_SOURCES := $(sort $(filter-out %_test.go,$(wildcard cmd/sc-agent/*.go) $(wildcard pkg/agent/*.go))) go.mod go.sum

# The committed guest agent's identity, and the reason this target is reproducible
# at all.
#
# The binary is committed to the repository and embedded in every image, so its
# bytes must be a function of this tree and the toolchain, and of nothing about
# the checkout that built it. Two build inputs break that when they come from git:
# `git describe --dirty` and the short commit both change with every commit and
# every dirty file, so a binary stamped with them can never be reproduced from the
# tree it is committed in — and this repository has the receipt, because the blob
# at pkg/image/binaries/sc-agent-amd64 was built two commits before the tree it
# landed in, carried a "-dirty" stamp, and nothing noticed across a release.
#
# Both stamps are therefore functions of the sources: AGENT_BUILD_ID is the first
# 16 hex characters of the SHA-256 of the files the binary is compiled from, in
# file order, and AGENT_VERSION is that same identity, because a release number
# cannot be read from this tree (the version the rest of the repository reports
# comes from git tags, which a committed binary cannot depend on).
AGENT_OUT := pkg/image/binaries/sc-agent-amd64
# Which SHA-256 tool is available, because the identity must be the same value on
# every host that builds this: Linux has sha256sum, a BSD or macOS host has shasum.
# Only the first 16 hex characters are used, so the two tools' output formats
# never reach the identity.
AGENT_SHA = $(shell command -v sha256sum >/dev/null 2>&1 && echo sha256sum || echo 'shasum -a 256')
AGENT_BUILD_ID := $(shell cat $(AGENT_SOURCES) 2>/dev/null | $(AGENT_SHA) | cut -c1-16)
AGENT_VERSION := src-$(AGENT_BUILD_ID)
AGENT_CHECK_OUT := $(BUILD_DIR)/sc-agent-amd64.rebuilt

# agent-build is the one recipe for the agent: the check target below must build
# exactly what the committed target builds, or it would be verifying something
# else.
#
# -trimpath keeps the build directory out of the binary and -buildvcs=false keeps
# this checkout's VCS state out of it (without it, `go build` stamps the module's
# pseudo-version — v0.13.1-0.20261110150542-cf787b52e943+dirty — into the binary,
# so the same sources build differently in a different checkout). With both, a
# copy of this tree at any path builds the same bytes, which is what makes the
# comparison below meaningful.
define agent-build
@mkdir -p $(dir $(1))
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(GO) build -trimpath -buildvcs=false \
	-ldflags "-s -w -X github.com/restuhaqza/swarmcracker/pkg/agent.AgentVersion=$(AGENT_VERSION) -X github.com/restuhaqza/swarmcracker/pkg/agent.BuildID=$(AGENT_BUILD_ID)" \
	-o $(1) ./cmd/sc-agent
endef

$(AGENT_OUT): $(AGENT_SOURCES)
	@echo "Building sc-agent-static-amd64..."
	$(call agent-build,$@)

# check-agent-binary fails unless the committed guest agent is the build of the
# sources in this tree.
#
# Nothing else in the repository reads that blob: pkg/image embeds it with
# go:embed, so a stale agent compiles, passes every test, and ships — which is how
# the drift described above survived. This target is the one place that compares
# it, and it has to rebuild rather than hash what is committed, because a hash of
# the blob only says the blob is unchanged.
#
# Byte-identical output needs the toolchain the committed agent was built with, so
# a mismatch reports both toolchains: a runner pinned to another Go patch release
# would otherwise look like a source drift.
.PHONY: check-agent-binary
check-agent-binary:
	@echo "Rebuilding the committed guest agent to compare it with its sources..."
	$(call agent-build,$(AGENT_CHECK_OUT))
	@if cmp -s $(AGENT_OUT) $(AGENT_CHECK_OUT); then \
		echo "✅ committed guest agent matches these sources (identity $(AGENT_BUILD_ID))"; \
		rm -f $(AGENT_CHECK_OUT); \
	else \
		echo "❌ committed guest agent is NOT the build of these sources"; \
		echo "   committed: $$(sha256sum $(AGENT_OUT) | cut -c1-32)... $$(wc -c < $(AGENT_OUT)) bytes, built with $$($(GO) version $(AGENT_OUT) 2>/dev/null | awk '{print $$NF}')"; \
		echo "   rebuilt:   $$(sha256sum $(AGENT_CHECK_OUT) | cut -c1-32)... $$(wc -c < $(AGENT_CHECK_OUT)) bytes, built with $$($(GO) env GOVERSION)"; \
		echo "   If the agent's sources changed, run 'make binaries' and commit $(AGENT_OUT) with them."; \
		echo "   If they did not change, the two builds above used different toolchains: byte-identical output needs the one the committed agent was built with."; \
		echo "   The rebuilt binary is left at $(AGENT_CHECK_OUT) for inspection."; \
		exit 1; \
	fi

# Multi-node development lab (creates nested VMs on this KVM host)
lab:
	@echo "Bringing up multi-node SwarmCracker lab..."
	sudo ./test-automation/multinode/cluster-lab.sh up $(or $(N),2)

lab-down:
	@echo "Destroying multi-node SwarmCracker lab..."
	sudo ./test-automation/multinode/cluster-lab.sh destroy

# Verify Go version alignment across all config files
check-go-version:
	@GO_MOD=$$(grep '^go ' go.mod | awk '{print $$2}'); \
	CI_GO=$$(grep 'go-version' .github/workflows/ci.yml | head -1 | grep -oP '[0-9]+\.[0-9]+'); \
	RELEASE_GO=$$(grep 'go-version' .github/workflows/release.yml | head -1 | grep -oP '[0-9]+\.[0-9]+'); \
	DOCKER_GO=$$(grep 'FROM golang:' Dockerfile | grep -oP '[0-9]+\.[0-9]+'); \
	if [ "$$GO_MOD" != "$$CI_GO" ] || [ "$$GO_MOD" != "$$RELEASE_GO" ] || [ "$$GO_MOD" != "$$DOCKER_GO" ]; then \
		echo "❌ Go version mismatch!"; \
		echo "   go.mod:     $$GO_MOD"; \
		echo "   CI:         $$CI_GO"; \
		echo "   Release:    $$RELEASE_GO"; \
		echo "   Dockerfile: $$DOCKER_GO"; \
		exit 1; \
	fi; \
	echo "✅ All Go versions aligned at $$GO_MOD"
