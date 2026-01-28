#!/bin/bash
set -e

# Resolve Hostname to IP
# Tries dig, getent, and python3 in order.
# Returns the first IP found.

HOSTNAME=$1

if [ -z "$HOSTNAME" ]; then
    echo "Usage: $0 <hostname>"
    exit 1
fi

# Check if input is already an IP
if echo "$HOSTNAME" | grep -E -q '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$'; then
    echo "$HOSTNAME"
    exit 0
fi

# Try dig
if command -v dig >/dev/null 2>&1; then
    IP=$(dig +short "$HOSTNAME" | head -n 1)
    if [ -n "$IP" ]; then
        echo "$IP"
        exit 0
    fi
fi

# Try getent (common on Linux/CI)
if command -v getent >/dev/null 2>&1; then
    IP=$(getent hosts "$HOSTNAME" | awk '{print $1}' | head -n 1)
    if [ -n "$IP" ]; then
        echo "$IP"
        exit 0
    fi
fi

# Try python3
if command -v python3 >/dev/null 2>&1; then
    IP=$(python3 -c "import socket; print(socket.gethostbyname('$HOSTNAME'))" 2>/dev/null || true)
    if [ -n "$IP" ]; then
        echo "$IP"
        exit 0
    fi
fi

echo "❌ Could not resolve IP for $HOSTNAME" >&2
exit 1
