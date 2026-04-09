#!/bin/bash
# SwarmCracker deployment benchmark - runs entirely inside the manager VM
# to eliminate SSH round-trip overhead and measure true SwarmKit latency.
# Run this from test-automation dir.

set -euo pipefail

RUN_ID=$(date +%Y%m%d-%H%M%S)
RESULTS="/tmp/sc-bench-$RUN_ID.csv"

echo "=========================================="
echo "  SwarmCracker Deployment Benchmark"
echo "  Run: $RUN_ID"
echo "  (executed inside manager VM)"
echo "=========================================="

# Execute benchmark script inside the manager VM
vagrant ssh manager -c 'bash -s' << 'SCRIPT' 2>&1 | sed '/^\[fog\]/d; /^==>/d; /^$/d'
set -euo pipefail

SWARMCTL="sudo SWARM_SOCKET=/var/run/swarmkit/swarm.sock /tmp/swarmctl"
RESULTS="/tmp/sc-bench-internal.csv"
rm -f "$RESULTS"

echo "test,image,action,duration_ms,duration_s" > "$RESULTS"

time_ms() {
    local start_ns=$(date +%s%N)
    "$@"
    local end_ns=$(date +%s%N)
    echo $(( (end_ns - start_ns) / 1000000 ))
}

time_s() {
    local start_ns=$(date +%s%N)
    "$@"
    local end_ns=$(date +%s%N)
    echo $(( (end_ns - start_ns) / 1000000000 ))
}

create_svc() {
    $SWARMCTL create-service "$1" 2>/dev/null
}

remove_svc() {
    $SWARMCTL rm-service "$1" 2>/dev/null || true
}

count_ready() {
    $SWARMCTL ls-tasks 2>/dev/null | grep -c "READY" || echo 0
}

get_svc_id() {
    $SWARMCTL ls-services 2>/dev/null | tail -1 | awk '{print $1}'
}

echo ""
echo "--- Cluster ---"
$SWARMCTL ls-nodes 2>/dev/null
echo ""

# Clean up stale services first
echo "Cleaning up stale services..."
for sid in $($SWARMCTL ls-services 2>/dev/null | awk 'NR>2{print $1}'); do
    $SWARMCTL rm-service "$sid" 2>/dev/null || true
done
sleep 2
echo "Clean. Active services: $($SWARMCTL ls-services 2>/dev/null | grep -c "svc-" || echo 0)"
echo ""

###############################################
echo "TEST 1: Alpine (single replica, ~7MB image)"
###############################################
MS=$(time_ms create_svc alpine:latest)
SID=$(get_svc_id)
echo "  Create: ${MS}ms | SID: ${SID:0:16}"
echo "t1,alpine:latest,create,$MS,0" >> "$RESULTS"

# Wait for READY
WS=$(time_s bash -c '
    for i in $(seq 1 120); do
        R=$('$SWARMCTL' ls-tasks 2>/dev/null | grep -c "READY" || echo 0)
        if [ "$R" -ge 1 ]; then echo "0"; exit 0; fi
        sleep 1
    done
    echo "120"
')
echo "  Ready: ${WS}s"
echo "t1,alpine:latest,wait_ready,$((WS*1000)),$WS" >> "$RESULTS"

# Show task detail
echo "  Tasks:"
$SWARMCTL ls-tasks 2>/dev/null | tail -5

MS=$(time_ms remove_svc "$SID")
echo "  Remove: ${MS}ms"
echo "t1,alpine:latest,remove,$MS,0" >> "$RESULTS"
echo ""

###############################################
echo "TEST 2: Nginx Alpine (~40MB image)"
###############################################
MS=$(time_ms create_svc nginx:alpine)
SID=$(get_svc_id)
echo "  Create: ${MS}ms | SID: ${SID:0:16}"
echo "t2,nginx:alpine,create,$MS,0" >> "$RESULTS"

WS=$(time_s bash -c '
    for i in $(seq 1 120); do
        R=$('$SWARMCTL' ls-tasks 2>/dev/null | grep -c "READY" || echo 0)
        if [ "$R" -ge 1 ]; then echo "0"; exit 0; fi
        sleep 1
    done
    echo "120"
')
echo "  Ready: ${WS}s"
echo "t2,nginx:alpine,wait_ready,$((WS*1000)),$WS" >> "$RESULTS"

echo "  Tasks:"
$SWARMCTL ls-tasks 2>/dev/null | tail -5

MS=$(time_ms remove_svc "$SID")
echo "  Remove: ${MS}ms"
echo "t2,nginx:alpine,remove,$MS,0" >> "$RESULTS"
echo ""

###############################################
echo "TEST 3: Redis Alpine (~30MB image)"
###############################################
MS=$(time_ms create_svc redis:alpine)
SID=$(get_svc_id)
echo "  Create: ${MS}ms | SID: ${SID:0:16}"
echo "t3,redis:alpine,create,$MS,0" >> "$RESULTS"

WS=$(time_s bash -c '
    for i in $(seq 1 120); do
        R=$('$SWARMCTL' ls-tasks 2>/dev/null | grep -c "READY" || echo 0)
        if [ "$R" -ge 1 ]; then echo "0"; exit 0; fi
        sleep 1
    done
    echo "120"
')
echo "  Ready: ${WS}s"
echo "t3,redis:alpine,wait_ready,$((WS*1000)),$WS" >> "$RESULTS"

echo "  Tasks:"
$SWARMCTL ls-tasks 2>/dev/null | tail -5

MS=$(time_ms remove_svc "$SID")
echo "  Remove: ${MS}ms"
echo "t3,redis:alpine,remove,$MS,0" >> "$RESULTS"
echo ""

###############################################
echo "TEST 4: Rapid Sequential (5x Alpine)"
###############################################
IDS=""
TOTAL_C=0
for i in 1 2 3 4 5; do
    MS=$(time_ms create_svc alpine:latest)
    SID=$(get_svc_id)
    IDS="$IDS $SID"
    TOTAL_C=$((TOTAL_C + MS))
    echo "  #$i create: ${MS}ms | SID: ${SID:0:16}"
    echo "t4r$i,alpine:latest,create,$MS,0" >> "$RESULTS"
done
echo "  Total create: ${TOTAL_C}ms | Avg: $((TOTAL_C/5))ms"
echo "t4_total,alpine:latest,total_create,$TOTAL_C,0" >> "$RESULTS"

sleep 10
echo "  Tasks after 10s:"
$SWARMCTL ls-tasks 2>/dev/null | tail -8

TOTAL_R=0
for SID in $IDS; do
    MS=$(time_ms remove_svc "$SID")
    TOTAL_R=$((TOTAL_R + MS))
done
echo "  Total remove: ${TOTAL_R}ms"
echo "t4_total,alpine:latest,total_remove,$TOTAL_R,0" >> "$RESULTS"
echo ""

###############################################
echo "TEST 5: Node.js 18 Alpine (~170MB image)"
###############################################
MS=$(time_ms create_svc node:18-alpine)
SID=$(get_svc_id)
echo "  Create: ${MS}ms | SID: ${SID:0:16}"
echo "t5,node:18-alpine,create,$MS,0" >> "$RESULTS"

WS=$(time_s bash -c '
    for i in $(seq 1 180); do
        R=$('$SWARMCTL' ls-tasks 2>/dev/null | grep -c "READY" || echo 0)
        if [ "$R" -ge 1 ]; then echo "0"; exit 0; fi
        sleep 1
    done
    echo "180"
')
echo "  Ready: ${WS}s"
echo "t5,node:18-alpine,wait_ready,$((WS*1000)),$WS" >> "$RESULTS"

echo "  Tasks:"
$SWARMCTL ls-tasks 2>/dev/null | tail -5

MS=$(time_ms remove_svc "$SID")
echo "  Remove: ${MS}ms"
echo "t5,node:18-alpine,remove,$MS,0" >> "$RESULTS"
echo ""

###############################################
echo "TEST 6: Postgres 15 Alpine (~80MB image)"
###############################################
MS=$(time_ms create_svc postgres:15-alpine)
SID=$(get_svc_id)
echo "  Create: ${MS}ms | SID: ${SID:0:16}"
echo "t6,postgres:15-alpine,create,$MS,0" >> "$RESULTS"

WS=$(time_s bash -c '
    for i in $(seq 1 180); do
        R=$('$SWARMCTL' ls-tasks 2>/dev/null | grep -c "READY" || echo 0)
        if [ "$R" -ge 1 ]; then echo "0"; exit 0; fi
        sleep 1
    done
    echo "180"
')
echo "  Ready: ${WS}s"
echo "t6,postgres:15-alpine,wait_ready,$((WS*1000)),$WS" >> "$RESULTS"

echo "  Tasks:"
$SWARMCTL ls-tasks 2>/dev/null | tail -5

MS=$(time_ms remove_svc "$SID")
echo "  Remove: ${MS}ms"
echo "t6,postgres:15-alpine,remove,$MS,0" >> "$RESULTS"
echo ""

###############################################
echo "TEST 7: Repeated Alpine (5 runs for average)"
###############################################
CREATE_TIMES=""
WAIT_TIMES=""
REMOVE_TIMES=""
for run in 1 2 3 4 5; do
    CMS=$(time_ms create_svc alpine:latest)
    SID=$(get_svc_id)

    WS=$(time_s bash -c '
        for i in $(seq 1 120); do
            R=$('$SWARMCTL' ls-tasks 2>/dev/null | grep -c "READY" || echo 0)
            if [ "$R" -ge 1 ]; then echo "0"; exit 0; fi
            sleep 1
        done
        echo "120"
    ')

    RMS=$(time_ms remove_svc "$SID")

    CREATE_TIMES="$CREATE_TIMES $CMS"
    WAIT_TIMES="$WAIT_TIMES $WS"
    REMOVE_TIMES="$REMOVE_TIMES $RMS"
    echo "  Run $run: create=${CMS}ms wait=${WS}s remove=${RMS}ms"
    echo "t7r$run,alpine:latest,create,$CMS,0" >> "$RESULTS"
    echo "t7r$run,alpine:latest,wait_ready,$((WS*1000)),$WS" >> "$RESULTS"
    echo "t7r$run,alpine:latest,remove,$RMS,0" >> "$RESULTS"
done

echo ""
echo "  Repeated Alpine averages:"
# Parse arrays for averages
CREATE_AVG=0; WAIT_AVG=0; REMOVE_AVG=0; COUNT=0
for c in $CREATE_TIMES; do CREATE_AVG=$((CREATE_AVG + c)); COUNT=$((COUNT+1)); done
for w in $WAIT_TIMES; do WAIT_AVG=$((WAIT_AVG + w)); done
for r in $REMOVE_TIMES; do REMOVE_AVG=$((REMOVE_AVG + r)); done
echo "  Create avg: $((CREATE_AVG/COUNT))ms"
echo "  Wait avg: $((WAIT_AVG/COUNT))s"
echo "  Remove avg: $((REMOVE_AVG/COUNT))ms"
echo "t7_avg,alpine:latest,avg_create,$((CREATE_AVG/COUNT)),0" >> "$RESULTS"
echo "t7_avg,alpine:latest,avg_wait_ready,$((WAIT_AVG/COUNT*1000)),$((WAIT_AVG/COUNT))" >> "$RESULTS"
echo "t7_avg,alpine:latest,avg_remove,$((REMOVE_AVG/COUNT)),0" >> "$RESULTS"

###############################################
echo ""
echo "=========================================="
echo "  FINAL TASK STATE"
echo "=========================================="
$SWARMCTL ls-tasks 2>/dev/null

echo ""
echo "=========================================="
echo "  RAW CSV DATA"
echo "=========================================="
cat "$RESULTS"

SCRIPT
