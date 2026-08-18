FROM golang:1.26.6 AS builder
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY main.go .
RUN CGO_ENABLED=0 GOOS=linux go build -o perf-mock-server .

FROM alpine:3.22.1
RUN apk --no-cache add ca-certificates \
    && addgroup -S appgroup \
    && adduser -S -G appgroup -u 65532 appuser
WORKDIR /app
COPY --from=builder /workspace/perf-mock-server .
RUN chown appuser:appgroup perf-mock-server
USER 65532
CMD ["./perf-mock-server", "--addr=:8080"]
