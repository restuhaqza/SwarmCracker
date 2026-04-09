#!/bin/bash
# SwarmCracker Deployment Benchmark v4 - clean version
# Runs inside manager VM
set -u

SWARMCTL="sudo SWARM_SOCKET=/var/run/swarmkit/swarm.sock /tmp/swarmctl"
RESULTS="/tmp/sc-bench.csv"
rm -f "$RESULTS"
echo "test,image,action,duration_ms" > "$RESULTS"

echo "=========================================="
echo "  SwarmCracker Deployment Benchmark"
echo "  $(date +%Y%m%d-%H%M%S)"
echo "=========================================="
echo ""

echo "--- Cluster ---"
$SWARMCTL ls-nodes
echo ""

echo "--- Existing Services ---"
$SWARMCTL ls-services
echo ""

# Utility: measure command time, capture output separately
measure_ms() {
    local s ns e
    ns=$(date +%s%N)
    "$@" > /tmp/sc-out 2>&1
    e=$(date +%s%N)
    echo $(( (e - ns) / 1000000 ))
}

# Utility: get last service ID from listing
get_last_sid() {
    $SWARMCTL ls-services 2>/dev/null | awk 'NR>2{print $1}' | tail -1
}

# Utility: count READY tasks
count_ready() {
    $SWARMCTL ls-tasks 2>/dev/null | grep -c "READY"
}

# Utility: wait for N READY tasks, returns seconds waited
wait_ready() {
    local needed=${1:-1} timeout=${2:-120}
    local s=$(date +%s)
    for i in $(seq 1 $timeout); do
        if [ "$(count_ready)" -ge "$needed" ]; then
            echo $(( $(date +%s) - s ))
            return 0
        fi
        sleep 1
    done
    echo $(( $(date +%s) - s ))
}

# Run a single deploy/remove cycle
run_test() {
    local label="$1" image="$2"
    echo "--- $label ($image) ---"
    
    local ms=$(measure_ms $SWARMCTL create-service "$image")
    local sid=$(get_last_sid)
    echo "  Create: ${ms}ms | SID: ${sid:0:16}"
    echo "$label,$image,create,$ms" >> "$RESULTS"
    
    local ws=$(wait_ready 1 90)
    echo "  Ready: ${ws}s"
    echo "$label,$image,wait_ready,$((ws*1000))" >> "$RESULTS"
    
    local rm_ms=$(measure_ms $SWARMCTL rm-service "$sid")
    echo "  Remove: ${rm_ms}ms"
    echo "$label,$image,remove,$rm_ms" >> "$RESULTS"
    echo ""
}

# ========================================
echo "=== TEST 1: Alpine (~7MB) ==="
run_test "t1_alpine" "alpine:latest"

echo "=== TEST 2: Nginx Alpine (~40MB) ==="
run_test "t2_nginx" "nginx:alpine"

echo "=== TEST 3: Redis Alpine (~30MB) ==="
run_test "t3_redis" "redis:alpine"

# ========================================
echo "=== TEST 4: Rapid Sequential (5x Alpine) ==="
IDS=""
TOTAL_C=0
for i in 1 2 3 4 5; do
    ms=$(measure_ms $SWARMCTL create-service alpine:latest)
    sid=$(get_last_sid)
    IDS="$IDS $sid"
    TOTAL_C=$((TOTAL_C + ms))
    echo "  #$i create: ${ms}ms | SID: ${sid:0:16}"
    echo "t4_r$i,alpine:latest,create,$ms" >> "$RESULTS"
done
echo "  Total: ${TOTAL_C}ms | Avg: $((TOTAL_C/5))ms"
echo "t4_total,alpine:latest,total_create,$TOTAL_C" >> "$RESULTS"

sleep 5
echo "  Tasks (last 8):"
$SWARMCTL ls-tasks | tail -8

TOTAL_R=0
for SID in $IDS; do
    ms=$(measure_ms $SWARMCTL rm-service "$SID")
    TOTAL_R=$((TOTAL_R + ms))
done
echo "  Total remove: ${TOTAL_R}ms"
echo "t4_total,alpine:latest,total_remove,$TOTAL_R" >> "$RESULTS"
echo ""

# ========================================
echo "=== TEST 5: Node.js 18 Alpine (~170MB) ==="
run_test "t5_node" "node:18-alpine"

echo "=== TEST 6: Postgres 15 Alpine (~80MB) ==="
run_test "t6_pg" "postgres:15-alpine"

# ========================================
echo "=== TEST 7: Repeated Alpine (5 runs) ==="
CS=0 WS=0 RS=0
for run in 1 2 3 4 5; do
    cms=$(measure_ms $SWARMCTL create-service alpine:latest)
    sid=$(get_last_sid)
    ws=$(wait_ready 1 90)
    rms=$(measure_ms $SWARMCTL rm-service "$sid")
    CS=$((CS + cms)); WS=$((WS + ws)); RS=$((RS + rms))
    echo "  Run $run: create=${cms}ms wait=${ws}s remove=${rms}ms"
    echo "t7r$run,alpine:latest,create,$cms" >> "$RESULTS"
    echo "t7r$run,alpine:latest,wait_ready,$((ws*1000))" >> "$RESULTS"
    echo "t7r$run,alpine:latest,remove,$rms" >> "$RESULTS"
done
echo "  Avg: create=$((CS/5))ms wait=$((WS/5))s remove=$((RS/5))ms"
echo "t7_avg,alpine:latest,avg_create,$((CS/5))" >> "$RESULTS"
echo "t7_avg,alpine:latest,avg_wait,$((WS/5*1000))" >> "$RESULTS"
echo "t7_avg,alpine:latest,avg_remove,$((RS/5))" >> "$RESULTS"
echo ""

# ========================================
echo "=========================================="
echo "  FINAL STATE"
echo "=========================================="
echo "Services:"
$SWARMCTL ls-services
echo ""
echo "Tasks:"
$SWARMCTL ls-tasks | tail -15

echo ""
echo "=========================================="
echo "  CSV RESULTS"
echo "=========================================="
cat "$RESULTS"
