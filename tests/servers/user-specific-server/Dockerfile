# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Copy root module (user-specific-server depends on internal packages)
COPY go.mod go.sum ./
RUN go mod download

COPY api/ api/
COPY internal/ internal/
COPY cmd/ cmd/
COPY tests/servers/user-specific-server/ tests/servers/user-specific-server/

RUN CGO_ENABLED=0 GOOS=linux go build -o /mcp-test-server ./tests/servers/user-specific-server/

FROM alpine:latest
COPY --from=builder /mcp-test-server /mcp-test-server
CMD ["/mcp-test-server"]
