# Build stage
FROM golang:1.26.5-alpine AS builder

WORKDIR /app

# Copy root module (server2 depends on internal packages)
COPY go.mod go.sum ./
RUN go mod download

COPY api/ api/
COPY internal/ internal/
COPY cmd/ cmd/
COPY tests/servers/server2/ tests/servers/server2/

RUN CGO_ENABLED=0 GOOS=linux go build -o /mcp-test-server ./tests/servers/server2/

FROM alpine:3.22.1
RUN addgroup -S appgroup \
    && adduser -S -G appgroup -u 65532 appuser
COPY --from=builder --chown=appuser:appgroup /mcp-test-server /mcp-test-server
USER 65532
CMD ["/mcp-test-server"]
