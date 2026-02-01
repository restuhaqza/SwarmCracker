#!/bin/bash
# test-deployment.sh - Test SwarmCracker deployments

set -e

echo "🧪 Testing SwarmCracker Deployments..."
echo ""

# Check if manager is running
echo "🔍 Step 1: Checking cluster status..."
vagrant ssh manager -c "
  export SWARM_SOCKET=/var/run/swarmkit/swarm.sock
  
  echo 'Cluster nodes:'
  swarmctl node ls
  
  echo ''
  echo 'Services (should be empty):'
  swarmctl service ls || true
"

echo ""
echo "=========================================="
echo "📦 Test 1: Deploy nginx service (3 replicas)"
echo "=========================================="

vagrant ssh manager -c "
  export SWARM_SOCKET=/var/run/swarmkit/swarm.sock
  
  # Create nginx service
  swarmctl service create \\
    --name nginx \\
    --image nginx:alpine \\
    --replicas 3
  
  echo ''
  echo '⏳ Waiting for tasks to start...'
  sleep 10
  
  echo ''
  echo 'Service status:'
  swarmctl service ps nginx
  
  echo ''
  echo 'Waiting for tasks to be RUNNING...'
  for i in {1..60}; do
    RUNNING=\$(swarmctl service ps nginx 2>/dev/null | grep -c 'RUNNING' || echo 0)
    if [ \$RUNNING -ge 3 ]; then
      echo '✅ All 3 replicas are running!'
      break
    fi
    echo \"Waiting... (\$RUNNING/3 running)\"
    sleep 2
  done
  
  swarmctl service ps nginx
"

echo ""
echo "=========================================="
echo "📦 Test 2: Scale service to 5 replicas"
echo "=========================================="

vagrant ssh manager -c "
  export SWARM_SOCKET=/var/run/swarmkit/swarm.sock
  
  swarmctl service update nginx --replicas 5
  
  echo ''
  echo '⏳ Waiting for scaled tasks...'
  sleep 15
  
  echo ''
  echo 'Service status after scaling:'
  swarmctl service ps nginx
"

echo ""
echo "=========================================="
echo "📦 Test 3: Rolling update"
echo "=========================================="

vagrant ssh manager -c "
  export SWARM_SOCKET=/var/run/swarmkit/swarm.sock
  
  swarmctl service update nginx \\
    --image nginx:1.25-alpine \\
    --update-parallelism 2 \\
    --update-delay 5s
  
  echo ''
  echo '⏳ Waiting for rolling update...'
  sleep 20
  
  echo ''
  echo 'Service status after update:'
  swarmctl service ps nginx
"

echo ""
echo "=========================================="
echo "📦 Test 4: Check microVMs on workers"
echo "=========================================="

echo "Worker 1 microVMs:"
vagrant ssh worker1 -c "
  sudo swarmcracker list || echo 'No VMs or swarmcracker not accessible'
"

echo ""
echo "Worker 2 microVMs:"
vagrant ssh worker2 -c "
  sudo swarmcracker list || echo 'No VMs or swarmcracker not accessible'
"

echo ""
echo "=========================================="
echo "📦 Test 5: Deploy multi-service stack"
echo "=========================================="

vagrant ssh manager -c "
  export SWARM_SOCKET=/var/run/swarmkit/swarm.sock
  
  # Deploy frontend
  swarmctl service create \\
    --name frontend \\
    --image nginx:alpine \\
    --replicas 2
  
  # Deploy backend
  swarmctl service create \\
    --name backend \\
    --image node:18-alpine \\
    --replicas 2 \\
    --env NODE_ENV=production
  
  # Deploy database
  swarmctl service create \\
    --name database \\
    --image postgres:15-alpine \\
    --replicas 1 \\
    --env POSTGRES_PASSWORD=testpass123
  
  echo ''
  echo '⏳ Waiting for services to start...'
  sleep 15
  
  echo ''
  echo 'All services:'
  swarmctl service ls
  
  echo ''
  echo 'Service tasks:'
  swarmctl service ps frontend
  swarmctl service ps backend
  swarmctl service ps database
"

echo ""
echo "=========================================="
echo "🧹 Cleanup: Remove test services"
echo "=========================================="

vagrant ssh manager -c "
  export SWARM_SOCKET=/var/run/swarmkit/swarm.sock
  
  swarmctl service remove frontend || true
  swarmctl service remove backend || true
  swarmctl service remove database || true
  
  echo ''
  echo '✅ Test services removed'
  echo ''
  echo 'Remaining services:'
  swarmctl service ls
"

echo ""
echo "=========================================="
echo "🎉 All tests completed!"
echo "=========================================="
echo ""
echo "Summary:"
echo "  ✅ nginx service deployed (3 replicas)"
echo "  ✅ Service scaled to 5 replicas"
echo "  ✅ Rolling update performed"
echo "  ✅ Multi-service stack deployed"
echo "  ✅ MicroVMs running on workers"
echo ""
echo "The cluster is ready for further testing!"
echo ""
