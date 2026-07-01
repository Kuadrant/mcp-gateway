# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Copy Go dependencies first to leverage Docker cache
COPY go.mod ./

RUN go mod download

# Copy the rest of the application source code
COPY *.go ./

RUN CGO_ENABLED=0 GOOS=linux go build -o /a2a-test-server

CMD ["/a2a-test-server"]
