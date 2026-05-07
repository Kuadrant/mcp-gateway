#!/bin/bash
# captures pprof profiles at intervals during a test run.
# usage: capture-profiles.sh <output_dir> <pprof_url> <interval_seconds> <cpu_duration>
# stops when STOP_FILE is created.

set -uo pipefail

OUTDIR="${1:?usage: capture-profiles.sh <output_dir> <pprof_url> <interval> <cpu_duration>}"
PPROF_URL="${2:-http://localhost:6060}"
INTERVAL="${3:-60}"
CPU_DURATION="${4:-30}"
STOP_FILE="${OUTDIR}/profiles.stop"

mkdir -p "$OUTDIR"
rm -f "$STOP_FILE"

snap=0
while [ ! -f "$STOP_FILE" ]; do
    ts=$(date +%s)
    prefix="$OUTDIR/snap-${snap}-${ts}"

    echo "[profile] snapshot $snap at $(date +%H:%M:%S)"

    go tool pprof -proto -output "${prefix}-goroutine.pb.gz" \
        "${PPROF_URL}/debug/pprof/goroutine" 2>/dev/null &

    go tool pprof -proto -output "${prefix}-heap.pb.gz" \
        "${PPROF_URL}/debug/pprof/heap" 2>/dev/null &

    go tool pprof -proto -output "${prefix}-cpu.pb.gz" \
        "${PPROF_URL}/debug/pprof/profile?seconds=${CPU_DURATION}" 2>/dev/null &

    # goroutine debug=2 gives full stack traces with state (locked, waiting, etc)
    curl -s "${PPROF_URL}/debug/pprof/goroutine?debug=2" > "${prefix}-goroutine-stacks.txt" 2>/dev/null &

    wait
    snap=$((snap + 1))
    sleep "$INTERVAL"
done

echo "[profile] stopped after $snap snapshots"
