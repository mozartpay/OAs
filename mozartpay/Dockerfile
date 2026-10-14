# Multi-service Dockerfile for building all MozartPay components
FROM golang:1.26-alpine AS base-builder

WORKDIR /app
RUN apk add --no-cache git ca-certificates tzdata
COPY go.mod go.sum ./
RUN go mod download

# MCP Server Builder  
FROM base-builder AS mcp-builder
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o mozartpay ./cmd/mozartpay

# CLI Builder
FROM base-builder AS cli-builder
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o mozartpay-cli ./cmd/mozartpay

# MCP Server Final Image
FROM alpine:latest AS mcp
RUN apk --no-cache add ca-certificates tzdata wget
WORKDIR /root/
COPY --from=mcp-builder /app/mozartpay .
RUN mkdir -p /root/.mozartpay
EXPOSE 3000
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget --no-verbose --tries=1 --spider http://localhost:3000/health || exit 1
CMD ["./mozartpay", "mcp", "--transport", "sse", "--port", "3000"]

# CLI Final Image
FROM alpine:latest AS cli
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /root/
COPY --from=cli-builder /app/mozartpay-cli .
RUN mkdir -p /root/.mozartpay
CMD ["./mozartpay-cli"]

# VC API Server Final Image
FROM alpine:latest AS vc-api
RUN apk --no-cache add ca-certificates tzdata wget
WORKDIR /root/
COPY --from=cli-builder /app/mozartpay-cli .
RUN mkdir -p /root/.mozartpay
EXPOSE 4000
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget --no-verbose --tries=1 --spider http://localhost:4000/health || exit 1
CMD ["./mozartpay-cli", "vc-api", "--port", "4000"]
