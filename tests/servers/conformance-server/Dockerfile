FROM mirror.gcr.io/node:24-alpine AS builder

# Install git to clone the upstream repo
RUN apk add --no-cache git

# Clone conformance repository and extract MCP conformance server
WORKDIR /tmp
RUN git clone --branch v0.1.9 --depth 1 https://github.com/modelcontextprotocol/conformance.git && \
    mkdir -p /app && \
    cp -r conformance/examples/servers/typescript/* /app

WORKDIR /app

RUN npm install

FROM mirror.gcr.io/node:24-alpine AS release

WORKDIR /app

COPY --from=builder /app/ ./

# Set environment variables
ENV NODE_ENV=production
ENV PORT=3000

# Start the server
CMD ["npm", "start"]
