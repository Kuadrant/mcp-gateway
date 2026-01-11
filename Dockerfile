FROM golang:1.25 AS builder

WORKDIR /workspace

COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY cmd/ cmd/
COPY internal/ internal/
COPY pkg/ pkg/
COPY api/ api/

RUN CGO_ENABLED=0 GOOS=linux go build -o mcp_gateway cmd/mcp-broker-router/main.go

FROM alpine:3.22.1

RUN apk --no-cache add ca-certificates

WORKDIR /app

COPY --from=builder /workspace/mcp_gateway .

RUN chmod +x mcp_gateway

# default to standalone mode with config file
# add the `--controller` flag for controller mode
CMD ["./mcp_gateway", "--mcp-gateway-config=/config/config.yaml"]
