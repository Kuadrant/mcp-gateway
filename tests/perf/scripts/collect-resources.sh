#!/bin/bash
# polls broker-router container resource usage via /proc and writes CSV.
# usage: collect-resources.sh <output.csv> [interval_seconds]
# stops when the file STOP_FILE is created or on SIGTERM.

set -euo pipefail

OUTPUT="${1:?usage: collect-resources.sh <output.csv> [interval]}"
INTERVAL="${2:-1}"
NAMESPACE="${MCP_GATEWAY_NAMESPACE:-mcp-system}"
DEPLOYMENT="${BROKER_ROUTER_NAME:-mcp-gateway}"
STOP_FILE="${OUTPUT}.stop"

rm -f "$STOP_FILE"
echo "timestamp,cpu_millicores,memory_rss_mb,memory_vms_mb,goroutines,threads" > "$OUTPUT"

prev_cpu=0
prev_ts=0

while [ ! -f "$STOP_FILE" ]; do
    ts=$(date +%s)

    # read /proc stats from the container (pid 1)
    stats=$(kubectl exec -n "$NAMESPACE" "deployment/$DEPLOYMENT" -- sh -c '
        cpu=$(cat /proc/1/stat | awk "{print \$14+\$15}");
        rss=$(grep VmRSS /proc/1/status | awk "{print \$2}");
        vms=$(grep VmSize /proc/1/status | awk "{print \$2}");
        threads=$(grep Threads /proc/1/status | awk "{print \$2}");
        goroutines=$(cat /proc/1/fd 2>/dev/null | wc -l);
        echo "$cpu $rss $vms $threads"
    ' 2>/dev/null) || { sleep "$INTERVAL"; continue; }

    cpu_ticks=$(echo "$stats" | awk '{print $1}')
    rss_kb=$(echo "$stats" | awk '{print $2}')
    vms_kb=$(echo "$stats" | awk '{print $3}')
    threads=$(echo "$stats" | awk '{print $4}')

    # calculate cpu millicores from tick delta
    cpu_mc=0
    if [ "$prev_ts" -gt 0 ] 2>/dev/null; then
        dt=$((ts - prev_ts))
        if [ "$dt" -gt 0 ]; then
            dticks=$((cpu_ticks - prev_cpu))
            # ticks are in clock ticks (usually 100/s), convert to millicores
            cpu_mc=$(( dticks * 1000 / (dt * 100) ))
        fi
    fi
    prev_cpu=$cpu_ticks
    prev_ts=$ts

    rss_mb=$(echo "scale=1; $rss_kb / 1024" | bc)
    vms_mb=$(echo "scale=1; $vms_kb / 1024" | bc)

    # get goroutine count from pprof if available (debug=1 returns text format)
    goroutines=$(curl -s "http://localhost:6060/debug/pprof/goroutine?debug=1" 2>/dev/null | head -1 | grep -o '[0-9]*' | head -1 || echo "0")
    if [ -z "$goroutines" ] || [ "$goroutines" = "0" ]; then
        goroutines="$threads"
    fi

    echo "$ts,$cpu_mc,$rss_mb,$vms_mb,$goroutines,$threads" >> "$OUTPUT"
    sleep "$INTERVAL"
done
