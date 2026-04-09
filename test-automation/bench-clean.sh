#!/bin/bash
# Clean benchmark - runs inside manager VM
# Prerequisites: stale services cleaned, only fresh tests
set -u

SWARMCTL="sudo SWARM_SOCKET=/var/run/swarmkit/swarm.sock /tmp/swarmctl"
RESULTS="/tmp/sc-bench-clean.csv"
rm -f "$RESULTS"
echo "test,image,action,duration_ms" > "$RESULTS"

measure_ms() {
    local s=$(date +%s%N)
    "$@" > /tmp/sc-out 2>&1
    local e=$(date +%s%N)
    echo $(( (e - s) / 1000000 ))
}

last_sid() { $SWARMCTL ls-services 2>/dev/null | awk 'NR>2{print $1}' | tail -1; }
count_state() { $SWARMCTL ls-tasks 2>/dev/null | awk "NR>2 && \$3==\"$1\"" | wc -l; }
count_ready() { count_state "READY"; }
count_running() { count_state "RUNNING"; }

wait_for_state() {
    local state="$1" needed="${2:-1}" timeout="${3:-120}"
    local s=$(date +%s)
    for i in $(seq 1 $timeout); do
        local c=$(count_state "$state")
        if [ "$c" -ge "$needed" ]; then
            echo $(( $(date +%s) - s ))
            return 0
        fi
        sleep 1
    done
    local e=$(date +%s)
    echo $(( e - s ))
}

echo "=========================================="
echo "  SwarmCracker Clean Benchmark"
echo "  $(date +%Y%m%d-%H%M%S)"
echo "=========================================="
echo ""

echo "--- Cluster ---"
$SWARMCTL ls-nodes
echo ""

echo "--- Cleaning ALL services ---"
for sid in $($SWARMCTL ls-services | awk 'NR>2{print $1}'); do
    $SWARMCTL rm-service "$sid" 2>/dev/null &
done
wait
sleep 10
echo "Remaining: $($SWARMCTL ls-services | awk 'NR>2' | wc -l)"
echo ""

# ============================================
echo "=========================================="
echo "  TEST 1: Alpine (cold start)"
echo "=========================================="
echo "--- t1_alpine (alpine:latest) ---"

# Measure create
ms=$(measure_ms $SWARMCTL create-service alpine:latest)
sid=$(last_sid)
echo "  Create: ${ms}ms | SID: ${sid:0:16}"
echo "t1_alpine,alpine:latest,create,$ms" >> "$RESULTS"

# Wait for READY
ws=$(wait_for_state "READY" 1 90)
echo "  READY in ${ws}s"
echo "t1_alpine,alpine:latest,wait_ready,$((ws*1000))" >> "$RESULTS"

# Wait for RUNNING
rs=$(wait_for_state "RUNNING" 1 90)
echo "  RUNNING in ${rs}s (from READY: $((rs-ws))s)"
echo "t1_alpine,alpine:latest,wait_running,$((rs*1000))" >> "$RESULTS"

# Show task
echo "  Task:"
$SWARMCTL ls-tasks | grep -v "^ID\|^$\|^Total" | tail -5

# Remove
ms=$(measure_ms $SWARMCTL rm-service "$sid")
echo "  Remove: ${ms}ms"
echo "t1_alpine,alpine:latest,remove,$ms" >> "$RESULTS"
echo ""

sleep 3

# ============================================
echo "=========================================="
echo "  TEST 2: Alpine (warm start - rootfs cached)"
echo "=========================================="
echo "--- t2_alpine_warm (alpine:latest) ---"

ms=$(measure_ms $SWARMCTL create-service alpine:latest)
sid=$(last_sid)
echo "  Create: ${ms}ms"
echo "t2_alpine_warm,alpine:latest,create,$ms" >> "$RESULTS"

ws=$(wait_for_state "READY" 1 90)
echo "  READY in ${ws}s"
echo "t2_alpine_warm,alpine:latest,wait_ready,$((ws*1000))" >> "$RESULTS"

rs=$(wait_for_state "RUNNING" 1 90)
echo "  RUNNING in ${rs}s"
echo "t2_alpine_warm,alpine:latest,wait_running,$((rs*1000))" >> "$RESULTS

echo "  Task:"
$SWARMCTL ls-tasks | grep -v "^ID\|^$\|^Total" | tail -3

ms=$(measure_ms $SWARMCTL rm-service "$sid")
echo "  Remove: ${ms}ms"
echo "t2_alpine_warm,alpine:latest,remove,$ms" >> "$RESULTS"
echo ""

sleep 3

# ============================================
echo "=========================================="
echo "  TEST 3: Repeated Alpine (3 runs)"
echo "=========================================="
CS=0 WS=0 RS=0
for run in 1 2 3; do
    echo "  Run $run..."
    cms=$(measure_ms $SWARMCTL create-service alpine:latest)
    sid=$(last_sid)
    ws=$(wait_for_state "READY" 1 90)
    rs=$(wait_for_state "RUNNING" 1 90)
    rms=$(measure_ms $SWARMCTL rm-service "$sid")
    CS=$((CS + cms)); WS=$((WS + ws)); RS=$((RS + rs))
    echo "    create=${cms}ms ready=${ws}s running=${rs}s remove=${rms}ms"
    echo "t3r$run,alpine:latest,create,$cms" >> "$RESULTS"
    echo "t3r$run,alpine:latest,wait_ready,$((ws*1000))" >> "$RESULTS"
    echo "t3r$run,alpine:latest,wait_running,$((rs*1000))" >> "$RESULTS"
    echo "t3r$run,alpine:latest,remove,$rms" >> "$RESULTS"
    sleep 3
done
echo "  Averages: create=$((CS/3))ms ready=$((WS/3))s running=$((RS/3))s remove=$((RS/3))ms"
echo "t3_avg,alpine:latest,avg_create,$((CS/3))" >> "$RESULTS"
echo "t3_avg,alpine:latest,avg_ready,$((WS/3*1000))" >> "$RESULTS"
echo "t3_avg,alpine:latest,avg_running,$((RS/3*1000))" >> "$RESULTS"
echo ""

# ============================================
echo "=========================================="
echo "  TEST 4: Scale-out (sequential 3 services)"
echo "=========================================="
T_CREATE=0
T_READY=0
T_RUNNING=0
SIDS=""
for i in 1 2 3; do
    echo "  Service #$i..."
    ms=$(measure_ms $SWARMCTL create-service alpine:latest)
    sid=$(last_sid)
    SIDS="$SIDS $sid"
    T_CREATE=$((T_CREATE + ms))
    ws=$(wait_for_state "READY" 1 60)
    rs=$(wait_for_state "RUNNING" 1 60)
    T_READY=$((T_READY + ws))
    T_RUNNING=$((T_RUNNING + rs))
    echo "    create=${ms}ms ready=${ws}s running=${rs}s"
    echo "t4s$i,alpine:latest,create,$ms" >> "$RESULTS"
    echo "t4s$i,alpine:latest,wait_running,$((rs*1000))" >> "$RESULTS"
done
echo "  Totals: create=${T_CREATE}ms avg_ready=$((T_READY/3))s avg_running=$((T_RUNNING/3))s"
echo "t4_total,alpine:latest,total_create,$T_CREATE" >> "$RESULTS"
echo "t4_total,alpine:latest,avg_running,$((T_RUNNING/3*1000))" >> "$RESULTS"

sleep 3
# Show all RUNNING tasks
echo "  All tasks:"
$SWARMCTL ls-tasks | grep "RUNNING"
RUNNING_COUNT=$(count_running)
echo "  Running count: $RUNNING_COUNT"

# Cleanup
T_REMOVE=0
for SID in $SIDS; do
    ms=$(measure_ms $SWARMCTL rm-service "$SID")
    T_REMOVE=$((T_REMOVE + ms))
done
echo "  Total remove: ${T_REMOVE}ms"
echo "t4_total,alpine:latest,total_remove,$T_REMOVE" >> "$RESULTS"
echo ""

# ============================================
echo "=========================================="
echo "  FINAL STATE"
echo "=========================================="
echo "Services:"
$SWARMCTL ls-services
echo ""
echo "Task summary:"
$SWARMCTL ls-tasks | awk 'NR>2' | awk '{print $3}' | sort | uniq -c | sort -rn

echo ""
echo "=========================================="
echo "  CSV RESULTS"
echo "=========================================="
cat "$RESULTS"
