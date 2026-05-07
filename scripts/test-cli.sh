#!/bin/bash
# Test script for swarmcracker-kit CLI

set -e

echo "==================================="
echo "SwarmCracker CLI Test Suite"
echo "==================================="
echo ""

BUILD_DIR="./build"
BINARY="$BUILD_DIR/swarmcracker-kit"
CONFIG="config.example.yaml"

# Check if binary exists
if [ ! -f "$BINARY" ]; then
    echo "❌ Binary not found. Building..."
    make all
fi

echo "✓ Binary found: $BINARY"
echo ""

# Test 1: Help
echo "Test 1: Display help"
echo "---------------------"
$BINARY --help | head -10
echo "✓ Help works"
echo ""

# Test 2: Version
echo "Test 2: Display version"
echo "------------------------"
$BINARY version
echo "✓ Version works"
echo ""

# Test 3: Validate command
echo "Test 3: Validate configuration"
echo "-------------------------------"
$BINARY validate --config "$CONFIG"
echo "✓ Validate works"
echo ""

# Test 4: Run --help
echo "Test 4: Run command help"
echo "------------------------"
$BINARY run --help | head -15
echo "✓ Run help works"
echo ""

# Test 5: Run test mode
echo "Test 5: Run in test mode"
echo "------------------------"
$BINARY run --config "$CONFIG" --test nginx:latest
echo "✓ Run test mode works"
echo ""

# Test 6: Run with custom flags
echo "Test 6: Run with custom resources"
echo "---------------------------------"
$BINARY run --config "$CONFIG" --test --vcpus 2 --memory 1024 nginx:latest
echo "✓ Run with custom flags works"
echo ""

# Test 7: Run with environment variables
echo "Test 7: Run with environment variables"
echo "--------------------------------------"
$BINARY run --config "$CONFIG" --test -e APP=prod -e DEBUG=false nginx:latest
echo "✓ Run with env vars works"
echo ""

# Test 8: Debug logging
echo "Test 8: Debug logging"
echo "---------------------"
$BINARY run --config "$CONFIG" --log-level debug --test nginx:latest 2>&1 | head -5
echo "✓ Debug logging works"
echo ""

echo "==================================="
echo "All tests passed! ✓"
echo "==================================="
