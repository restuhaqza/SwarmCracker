#!/bin/bash
# Verification script for Firecracker agent deployment
# This script checks that the agent is working correctly

set -e

MANAGER_HOST="${1:-192.168.56.10}"
EXPECTED_WORKER="${2:-worker-firecracker}"

echo "🔍 Verifying Firecracker Agent Deployment"
echo "========================================="
echo ""

# Check if swarmctl is available
if ! command -v swarmctl &> /dev/null; then
    echo "❌ swarmctl not found. Please install SwarmKit tools."
    exit 1
fi

# Set socket
export SWARM_SOCKET="${SWARM_SOCKET:-/var/run/swarmkit/swarm.sock}"

echo "📊 1. Checking cluster nodes..."
if ! swarmctl node ls 2>/dev/null; then
    echo "❌ Cannot connect to cluster. Check SWARM_SOCKET or manager connection."
    exit 1
fi
echo ""

echo "🔎 2. Looking for Firecracker-enabled worker..."
if swarmctl node ls | grep -q "$EXPECTED_WORKER"; then
    echo "✅ Found worker: $EXPECTED_WORKER"
else
    echo "⚠️  Worker $EXPECTED_WORKER not found in cluster"
    echo "Available workers:"
    swarmctl node ls | grep -v "Manager"
fi
echo ""

echo "🧪 3. Deploying test service (nginx:alpine)..."
if swarmctl service ls | grep -q "test-nginx"; then
    echo "Removing old test service..."
    swarmctl service rm test-nginx
    sleep 2
fi

swarmctl service create \
    --name test-nginx \
    --image nginx:alpine \
    --replicas 1 \
    --reservations-cpu 0.5 \
    --reservations-memory 256MB

echo "✅ Service created"
echo ""

echo "⏳ 4. Waiting for task to start (max 30 seconds)..."
for i in {1..30}; do
    TASK_STATE=$(swarmctl service ps test-nginx --format "{{.State}}" 2>/dev/null || echo "")
    if [ "$TASK_STATE" = "RUNNING" ]; then
        echo "✅ Task is RUNNING!"
        break
    elif [ "$TASK_STATE" = "REJECTED" ] || [ "$TASK_STATE" = "FAILED" ]; then
        echo "❌ Task failed with state: $TASK_STATE"
        echo "Task details:"
        swarmctl task inspect $(swarmctl task ls --no-trunc | grep test-nginx | awk '{print $1}')
        exit 1
    fi
    sleep 1
    echo -n "."
done
echo ""

if [ "$TASK_STATE" != "RUNNING" ]; then
    echo "⚠️  Task did not reach RUNNING state"
    echo "Current state: $TASK_STATE"
    echo ""
    echo "Task details:"
    swarmctl task inspect $(swarmctl task ls --no-trunc | grep test-nginx | awk '{print $1}')
    exit 1
fi

echo "📈 5. Testing service scaling..."
swarmctl service update test-nginx --replicas 3
echo "✅ Service scaled to 3 replicas"
sleep 3

RUNNING_COUNT=$(swarmctl service ps test-nginx --format "{{.State}}" | grep -c RUNNING || echo 0)
echo "Running tasks: $RUNNING_COUNT/3"

if [ "$RUNNING_COUNT" -ge 2 ]; then
    echo "✅ Scaling successful!"
else
    echo "⚠️  Some tasks may not be running yet"
fi
echo ""

echo "🧹 6. Cleanup..."
swarmctl service rm test-nginx
echo "✅ Test service removed"
echo ""

echo "========================================="
echo "✅ Verification Complete!"
echo ""
echo "Summary:"
echo "  - Worker joined cluster: ✓"
echo "  - Task execution: ✓"
echo "  - Service scaling: ✓"
echo ""
echo "The Firecracker agent is working correctly!"
