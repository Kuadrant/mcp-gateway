FROM mirror.gcr.io/node:24-alpine AS builder

# Install git to clone the upstream repo
RUN apk add --no-cache git

# Clone conformance repository and extract MCP conformance server
WORKDIR /tmp
RUN git clone --branch v0.1.16 --depth 1 https://github.com/modelcontextprotocol/conformance.git && \
    mkdir -p /app && \
    cp -r conformance/examples/servers/typescript/* /app

WORKDIR /app

# Replace the express app setup section to disable DNS rebinding protection.
# Upstream uses createMcpExpressApp() which rejects non-localhost Host
# headers, incompatible with in-cluster k8s service hostnames.
COPY start.ts /tmp/start.ts
RUN SPLIT=$(grep -n '// ===== EXPRESS APP =====' everything-server.ts | cut -d: -f1) && \
    head -n $((SPLIT - 1)) everything-server.ts > temp.ts && \
    cat /tmp/start.ts >> temp.ts && \
    mv temp.ts everything-server.ts

RUN npm install

FROM mirror.gcr.io/node:24-alpine AS release

WORKDIR /app

COPY --from=builder /app/ ./

# Set environment variables
ENV NODE_ENV=production
ENV PORT=3000

# Start the server
CMD ["npm", "start"]
