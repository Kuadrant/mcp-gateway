FROM golang:1.26.5 AS builder

WORKDIR /workspace

COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY cmd/ cmd/
COPY internal/ internal/
COPY api/ api/

ARG LDFLAGS=""
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags "${LDFLAGS}" -o mcp_gateway ./cmd/mcp-broker-router/

FROM alpine:3.22.1

RUN apk --no-cache add ca-certificates \
    && addgroup -S appgroup \
    && adduser -S -G appgroup -u 65532 appuser

WORKDIR /app

COPY --from=builder --chown=appuser:appgroup /workspace/mcp_gateway .

RUN chmod +x mcp_gateway

USER 65532

# default to standalone mode with config file
# add the `--controller` flag for controller mode
CMD ["./mcp_gateway", "--mcp-gateway-config=/config/config.yaml"]
