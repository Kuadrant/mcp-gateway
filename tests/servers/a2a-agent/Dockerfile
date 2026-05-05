FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -o /a2a-fixture-agent

FROM alpine:3.21
COPY --from=builder /a2a-fixture-agent /a2a-fixture-agent
CMD ["/a2a-fixture-agent"]
