#!/bin/bash
# Final clean benchmark - inside manager VM
set -u

SWARMCTL="sudo SWARM_SOCKET=/var/run/swarmkit/swarm.sock /tmp/swarmctl"
RESULTS="/tmp/sc-bench-final.csv"
rm -f "$RESULTS"
echo "test,image,action,duration_ms" > "$RESULTS"

measure_ms() {
    local s=$(date +%s%N)
    "$@" > /tmp/sc-out 2>&1
    local e=$(date +%s%N)
    echo $(( (e - s) / 1000000 ))
}

last_sid() { $SWARMCTL ls-services 2>/dev/null | awk 'NR>2{print $1}' | tail -1; }

count_state() {
    local state="$1"
    $SWARMCTL ls-tasks 2>/dev/null | awk -v s="$state" 'NR>2 && $3==s' | wc -l
}

wait_for() {
    local state="$1" needed="${2:-1}" timeout="${3:-120}"
    local s=$(date +%s)
    for i in $(seq 1 $timeout); do
        if [ "$(count_state "$state")" -ge "$needed" ]; then
            echo $(( $(date +%s) - s ))
            return 0
        fi
        sleep 1
    done
    echo $(( $(date +%s) - s ))
}

run_test() {
    local label="$1" image="$2"
    echo "=== $label ($image) ==="

    local ms=$(measure_ms $SWARMCTL create-service "$image")
    local sid=$(last_sid)
    echo "  Create: ${ms}ms | SID: ${sid:0:16}"
    echo "$label,$image,create,$ms" >> "$RESULTS"

    local ws=$(wait_for "READY" 1 90)
    echo "  READY: ${ws}s"
    echo "$label,$image,ready,$((ws*1000))" >> "$RESULTS"

    local rs=$(wait_for "RUNNING" 1 120)
    echo "  RUNNING: ${rs}s (boot: $((rs-ws))s)"
    echo "$label,$image,running,$((rs*1000))" >> "$RESULTS"

    echo "  Tasks:"
    $SWARMCTL ls-tasks | grep -E "READY|RUNNING" | tail -3

    local rms=$(measure_ms $SWARMCTL rm-service "$sid")
    echo "  Remove: ${rms}ms"
    echo "$label,$image,remove,$rms" >> "$RESULTS"
    echo ""
    sleep 5
}

echo "=========================================="
echo "  SwarmCracker FINAL Benchmark"
echo "  $(date +%Y%m%d-%H%M%S)"
echo "=========================================="
echo ""

echo "--- Cluster ---"
$SWARMCTL ls-nodes
echo ""

echo "--- Pre-check: no services ---"
$SWARMCTL ls-services
echo ""

# TEST 1: Cold start
run_test "t1_cold" "alpine:latest"

# TEST 2: Warm start (rootfs cached)
run_test "t2_warm" "alpine:latest"

# TEST 3: Another warm
run_test "t3_warm2" "alpine:latest"

# TEST 4: Repeated (3 runs)
echo "=== t4_repeated (alpine x3) ==="
CS=0 WS=0 RS=0
for run in 1 2 3; do
    cms=$(measure_ms $SWARMCTL create-service alpine:latest)
    sid=$(last_sid)
    ws=$(wait_for "READY" 1 90)
    rs=$(wait_for "RUNNING" 1 120)
    rms=$(measure_ms $SWARMCTL rm-service "$sid")
    CS=$((CS + cms)); WS=$((WS + ws)); RS=$((RS + rs))
    echo "  Run $run: create=${cms}ms ready=${ws}s running=${rs}s remove=${rms}ms"
    echo "t4r$run,alpine:latest,create,$cms" >> "$RESULTS"
    echo "t4r$run,alpine:latest,ready,$((ws*1000))" >> "$RESULTS"
    echo "t4r$run,alpine:latest,running,$((rs*1000))" >> "$RESULTS"
    echo "t4r$run,alpine:latest,remove,$rms" >> "$RESULTS"
    sleep 5
done
echo "  Avg: create=$((CS/3))ms ready=$((WS/3))s running=$((RS/3))s remove=$((RS/3))ms"
echo "t4_avg,alpine:latest,avg_create,$((CS/3))" >> "$RESULTS"
echo "t4_avg,alpine:latest,avg_ready,$((WS/3*1000))" >> "$RESULTS"
echo "t4_avg,alpine:latest,avg_running,$((RS/3*1000))" >> "$RESULTS"
echo "t4_avg,alpine:latest,avg_remove,$((RS/3))" >> "$RESULTS"
echo ""

echo "=========================================="
echo "  FINAL STATE"
echo "=========================================="
$SWARMCTL ls-tasks | awk 'NR>2' | awk '{print $3}' | sort | uniq -c | sort -rn

echo ""
echo "=========================================="
echo "  CSV RESULTS"
echo "=========================================="
cat "$RESULTS"
