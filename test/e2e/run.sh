#!/bin/bash
# E2E Test Runner for SwarmCracker
# Runs full end-to-end tests with proper setup and teardown

set -e

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Configuration
SWARMCTL_SOCKET="${SWARMCTL_SOCKET:-/var/run/swarmkit/swarm.sock}"
LOG_DIR="${LOG_DIR:-/tmp/swarmcracker-e2e-logs}"
TEST_TIMEOUT="${TEST_TIMEOUT:-10m}"

# Functions
log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

check_prerequisites() {
    log_info "Checking prerequisites..."
    
    # Check swarmd
    if ! command -v swarmd &> /dev/null; then
        log_error "swarmd not found. Install from: https://github.com/moby/swarmkit"
        exit 1
    fi
    log_info "✓ swarmd found"
    
    # Check swarmctl
    if ! command -v swarmctl &> /dev/null; then
        log_error "swarmctl not found. Install from: https://github.com/moby/swarmkit"
        exit 1
    fi
    log_info "✓ swarmctl found"
    
    # Check Firecracker
    if ! command -v firecracker &> /dev/null; then
        log_error "Firecracker not found. Install from: https://github.com/firecracker-microvm/firecracker"
        exit 1
    fi
    log_info "✓ firecracker found"
    
    # Check container runtime
    if command -v docker &> /dev/null; then
        log_info "✓ docker found"
    elif command -v podman &> /dev/null; then
        log_info "✓ podman found"
    else
        log_error "No container runtime found (docker or podman required)"
        exit 1
    fi
    
    # Check KVM
    if [ -e /dev/kvm ]; then
        log_info "✓ KVM device available"
    else
        log_warn "KVM device not available - tests may fail"
    fi
    
    log_info "All prerequisites met"
}

setup_environment() {
    log_info "Setting up test environment..."
    
    # Create log directory
    mkdir -p "$LOG_DIR"
    
    # Start swarmd if not running
    if [ ! -S "$SWARMCTL_SOCKET" ]; then
        log_info "Starting swarmd..."
        mkdir -p $(dirname $SWARMCTL_SOCKET)
        swarmd --listen-remote-api 0.0.0.0:4242 \
               --state-dir /tmp/swarmkit-e2e \
               --manager \
               --addr localhost:4242 \
               --debug \
               > "$LOG_DIR/swarmd.log" 2>&1 &
        SWARMD_PID=$!
        echo $SWARMD_PID > "$LOG_DIR/swarmd.pid"
        
        # Wait for swarmd to be ready
        sleep 5
        
        if [ ! -S "$SWARMCTL_SOCKET" ]; then
            log_error "Failed to start swarmd"
            cat "$LOG_DIR/swarmd.log"
            exit 1
        fi
        log_info "swarmd started (PID: $SWARMD_PID)"
    else
        log_info "swarmd already running"
    fi
}

teardown_environment() {
    log_info "Tearing down test environment..."
    
    # Stop swarmd if we started it
    if [ -f "$LOG_DIR/swarmd.pid" ]; then
        SWARMD_PID=$(cat "$LOG_DIR/swarmd.pid")
        log_info "Stopping swarmd (PID: $SWARMD_PID)..."
        kill $SWARMD_PID 2>/dev/null || true
        rm -f "$LOG_DIR/swarmd.pid"
    fi
    
    # Cleanup test services
    log_info "Cleaning up test services..."
    export SWARM_SOCKET=$SWARMCTL_SOCKET
    swarmctl service ls -q | grep "^e2e-" | xargs -r swarmctl service rm 2>/dev/null || true
    
    # Cleanup Firecracker VMs
    log_info "Cleaning up Firecracker VMs..."
    pkill -9 firecracker 2>/dev/null || true
    
    # Cleanup jail directories
    rm -rf /srv/jailer/e2e-* 2>/dev/null || true
    
    log_info "Teardown complete"
}

run_tests() {
    log_info "Running E2E tests..."
    
    # Set environment variables
    export SWARM_SOCKET=$SWARMCTL_SOCKET
    export SWARMCTL_SOCKET=$SWARMCTL_SOCKET
    
    # Run tests with timeout
    timeout "$TEST_TIMEOUT" go test ./test/e2e/... -v -timeout "$TEST_TIMEOUT" \
        -logdir="$LOG_DIR" \
        2>&1 | tee "$LOG_DIR/e2e-test.log"
    
    TEST_RESULT=${PIPESTATUS[0]}
    
    if [ $TEST_RESULT -eq 0 ]; then
        log_info "✓ All E2E tests passed"
    else
        log_error "✗ Some E2E tests failed"
        log_info "Check logs: $LOG_DIR/e2e-test.log"
    fi
    
    return $TEST_RESULT
}

generate_report() {
    log_info "Generating test report..."
    
    REPORT_FILE="$LOG_DIR/e2e-report.txt"
    
    cat > "$REPORT_FILE" <<EOF
SwarmCracker E2E Test Report
==========================
Date: $(date)
Test Log: $LOG_DIR/e2e-test.log
Swarmd Log: $LOG_DIR/swarmd.log

Test Results:
------------
$(grep -E "^(PASS|FAIL|SKIP)" "$LOG_DIR/e2e-test.log" | tail -20)

Summary:
--------
Total Tests: $(grep -c "^=== RUN" "$LOG_DIR/e2e-test.log" || echo "0")
Passed: $(grep -c "^--- PASS:" "$LOG_DIR/e2e-test.log" || echo "0")
Failed: $(grep -c "^--- FAIL:" "$LOG_DIR/e2e-test.log" || echo "0")
Skipped: $(grep -c "^--- SKIP:" "$LOG_DIR/e2e-test.log" || echo "0")

Logs:
------
Full test log: $LOG_DIR/e2e-test.log
Swarmd log: $LOG_DIR/swarmd.log
EOF

    cat "$REPORT_FILE"
    log_info "Report saved to: $REPORT_FILE"
}

# Trap for cleanup
trap teardown_environment EXIT INT TERM

# Main
main() {
    log_info "SwarmCracker E2E Test Runner"
    log_info "=============================="
    
    # Parse arguments
    SKIP_SETUP=false
    SKIP_TEARDOWN=false
    RUN_BENCHMARKS=false
    
    while [[ $# -gt 0 ]]; do
        case $1 in
            --skip-setup)
                SKIP_SETUP=true
                shift
                ;;
            --skip-teardown)
                SKIP_TEARDOWN=true
                shift
                ;;
            --benchmarks)
                RUN_BENCHMARKS=true
                shift
                ;;
            --help)
                echo "Usage: $0 [options]"
                echo "Options:"
                echo "  --skip-setup      Skip environment setup"
                echo "  --skip-teardown   Skip environment teardown"
                echo "  --benchmarks      Run benchmark tests"
                echo "  --help            Show this help"
                exit 0
                ;;
            *)
                log_error "Unknown option: $1"
                exit 1
                ;;
        esac
    done
    
    # Check prerequisites
    check_prerequisites
    
    # Setup environment
    if [ "$SKIP_SETUP" = false ]; then
        setup_environment
    fi
    
    # Run tests
    run_tests
    TEST_RESULT=$?
    
    # Generate report
    generate_report
    
    # Run benchmarks if requested
    if [ "$RUN_BENCHMARKS" = true ]; then
        log_info "Running benchmarks..."
        timeout "$TEST_TIMEOUT" go test ./test/e2e/... -bench=. -benchmem \
            -logdir="$LOG_DIR" \
            2>&1 | tee "$LOG_DIR/benchmark.log"
    fi
    
    # Skip teardown if requested
    if [ "$SKIP_TEARDOWN" = true ]; then
        trap - EXIT INT TERM
        log_warn "Skipping teardown (manual cleanup required)"
    fi
    
    exit $TEST_RESULT
}

main "$@"
