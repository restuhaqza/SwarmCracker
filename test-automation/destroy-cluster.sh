#!/bin/bash
# destroy-cluster.sh - Clean up and destroy the test cluster

set -e

echo "🧹 Destroying SwarmCracker Test Cluster..."
echo ""

# Check if we're in the right directory
if [ ! -f "Vagrantfile" ]; then
  echo "❌ Vagrantfile not found!"
  echo "Please run this script from the test-automation directory."
  exit 1
fi

# Option to remove services first
echo "🔍 Step 1: Checking if VMs are running..."
if vagrant status | grep -q "running"; then
  echo ""
  read -p "Remove services before destroying? (y/N): " -n 1 -r
  echo
  if [[ $REPLY =~ ^[Yy]$ ]]; then
    echo "🧹 Removing services..."
    vagrant ssh manager -c "
      export SWARM_SOCKET=/var/run/swarmkit/swarm.sock
      services=\$(swarmctl service ls -q 2>/dev/null)
      if [ -n \"\$services\" ]; then
        echo \$services | xargs -r swarmctl service remove
        echo '✅ Services removed'
      else
        echo 'No services to remove'
      fi
    " 2>/dev/null || echo "Manager not accessible or no services"
  fi
fi

echo ""
echo "🛑 Step 2: Stopping and destroying VMs..."
vagrant destroy -f

echo ""
echo "🗑️  Step 3: Cleaning up local files..."
# Remove .vagrant directory
rm -rf .vagrant

echo ""
echo "✅ Cluster destroyed successfully!"
echo ""
echo "To start fresh, run: ./start-cluster.sh"
echo ""
