FROM golang:1.26.6-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY api/ api/
COPY internal/ internal/
COPY cmd/ cmd/
COPY tests/servers/stateless-server/ tests/servers/stateless-server/

RUN CGO_ENABLED=0 GOOS=linux go build -o /mcp-test-server ./tests/servers/stateless-server/

FROM alpine:3.22.1
RUN addgroup -S appgroup \
    && adduser -S -G appgroup -u 65532 appuser
COPY --from=builder /mcp-test-server /mcp-test-server
RUN chown appuser:appgroup /mcp-test-server
USER 65532
CMD ["/mcp-test-server"]
