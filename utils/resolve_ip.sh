#!/bin/bash

# Copyright 2026.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -e

# Resolve Hostname to IP
# Tries dig, getent, and python3 in order.
# Returns the first IP found.

HOSTNAME=$1

if [ -z "$HOSTNAME" ]; then
    echo "Usage: $0 <hostname>"
    exit 1
fi
echo "Attempting to resolve hostname: $HOSTNAME" >&2

# Check if input is already an IP
if echo "$HOSTNAME" | grep -E -q '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$'; then
    echo "Input is already an IP: $HOSTNAME" >&2
    echo "$HOSTNAME"
    exit 0
fi

# Try dig
if command -v dig >/dev/null 2>&1; then
    echo "Trying 'dig'..." >&2
    IP=$(dig +short "$HOSTNAME" | head -n 1)
    if [ -n "$IP" ]; then
        echo "Resolved via 'dig': $IP" >&2
        echo "$IP"
        exit 0
    fi
else
    echo "'dig' not found." >&2
fi

# Try getent (common on Linux/CI)
if command -v getent >/dev/null 2>&1; then
    echo "Trying 'getent'..." >&2
    IP=$(getent hosts "$HOSTNAME" | awk '{print $1}' | head -n 1)
    if [ -n "$IP" ]; then
        echo "Resolved via 'getent': $IP" >&2
        echo "$IP"
        exit 0
    fi
else
    echo "'getent' not found." >&2
fi

# Try python3
if command -v python3 >/dev/null 2>&1; then
    echo "Trying 'python3'..." >&2
    IP=$(python3 -c "import socket; print(socket.gethostbyname('$HOSTNAME'))" 2>/dev/null || true)
    if [ -n "$IP" ]; then
        echo "Resolved via 'python3': $IP" >&2
        echo "$IP"
        exit 0
    fi
else
    echo "'python3' not found." >&2
fi

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
