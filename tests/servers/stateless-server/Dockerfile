FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY api/ api/
COPY internal/ internal/
COPY cmd/ cmd/
COPY tests/servers/stateless-server/ tests/servers/stateless-server/

RUN CGO_ENABLED=0 GOOS=linux go build -o /mcp-test-server ./tests/servers/stateless-server/

FROM alpine:latest
COPY --from=builder /mcp-test-server /mcp-test-server
CMD ["/mcp-test-server"]
