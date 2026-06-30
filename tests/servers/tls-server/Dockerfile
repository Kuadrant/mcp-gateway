FROM golang:1.25-alpine AS builder

WORKDIR /app
COPY go.mod go.sum* ./
RUN go mod download

COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -o tls-server .

FROM alpine:latest
RUN addgroup -S app && adduser -S app -G app
WORKDIR /app
COPY --from=builder /app/tls-server .
USER app
ENTRYPOINT ["/app/tls-server"]
