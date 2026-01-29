FROM golang:1.25-alpine AS builder

WORKDIR /app
COPY go.mod go.sum* ./
RUN go mod download

# Copy the rest of the application source code
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -o custom-path-server .

FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/custom-path-server .
ENTRYPOINT ["/app/custom-path-server", "-http", ":8080"]
