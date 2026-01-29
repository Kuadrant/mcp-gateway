# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Copy Go dependencies first to leverage Docker cache
COPY go.mod go.sum ./

RUN go mod download

# Copy the rest of the application source code
COPY *.go ./

RUN CGO_ENABLED=0 GOOS=linux go build -o /mcp-test-server

# Runtime stage
FROM alpine:latest

WORKDIR /app

# Copy the binary from builder stage
COPY --from=builder /mcp-test-server .

# Expose port
EXPOSE 9090

# Set default command  
CMD ["/app/mcp-test-server", "--http", "0.0.0.0:9090"]
