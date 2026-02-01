#!/bin/bash
# start-cluster.sh - Quick start script for the entire cluster

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

echo "🚀 Starting SwarmCracker Test Cluster..."
echo ""

# Check if Vagrant is installed
if ! command -v vagrant &> /dev/null; then
  echo "❌ Vagrant is not installed!"
  echo "Install it with: sudo apt-get install vagrant virtualbox"
  exit 1
fi

# Check if VirtualBox is installed
if ! command -v VBoxManage &> /dev/null; then
  echo "❌ VirtualBox is not installed!"
  echo "Install it with: sudo apt-get install virtualbox"
  exit 1
fi

# Make scripts executable
chmod +x scripts/*.sh

echo "📦 Step 1: Starting VMs with Vagrant..."
vagrant up

echo ""
echo "⏳ Step 2: Waiting for cluster to be ready..."
echo "This may take 5-10 minutes for provisioning..."
sleep 30

echo ""
echo "🔍 Step 3: Verifying cluster status..."
vagrant ssh manager -c "
  export SWARM_SOCKET=/var/run/swarmkit/swarm.sock
  echo 'Waiting for nodes to join...'
  for i in {1..30}; do
    NODE_COUNT=\$(swarmctl node ls 2>/dev/null | grep -c 'ACCEPTED\|READY' || echo 0)
    if [ \$NODE_COUNT -ge 3 ]; then
      echo '✅ All nodes joined!'
      break
    fi
    echo 'Waiting for nodes... (\$NODE_COUNT/3)'
    sleep 5
  done
  echo ''
  echo '📊 Cluster Status:'
  swarmctl node ls
"

echo ""
echo "=========================================="
echo "🎉 Cluster is ready!"
echo "=========================================="
echo ""
echo "Quick commands:"
echo "  vagrant ssh manager              - SSH into manager"
echo "  vagrant ssh worker1              - SSH into worker 1"
echo "  vagrant ssh worker2              - SSH into worker 2"
echo "  vagrant halt                     - Stop all VMs"
echo "  vagrant destroy -f               - Delete all VMs"
echo ""
echo "From manager node:"
echo "  export SWARM_SOCKET=/var/run/swarmkit/swarm.sock"
echo "  swarmctl node ls                 - List nodes"
echo "  swarmctl service ls              - List services"
echo ""
echo "Next: Run ./test-deployment.sh to test deployments"
echo ""
