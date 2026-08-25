# Multi-stage production build for Social Publishing MCP Server
# Stage 1: Build binary with zero CGO dependencies
FROM golang:1.25-alpine AS builder

WORKDIR /src

# Install git and CA certificates for module verification
RUN apk add --no-cache git ca-certificates tzdata

# Cache Go modules layer
COPY go.mod go.sum ./
RUN go mod download

# Copy application source code
COPY . .

# Compile stripped production binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-s -w -X main.version=1.0.0" \
    -trimpath \
    -o /bin/social-mcp-server ./cmd/server

# Stage 2: Minimal, hardened, non-root runtime container
FROM alpine:3.20

# Install runtime security necessities
RUN apk --no-cache add ca-certificates tzdata curl && \
    addgroup -g 10001 appgroup && \
    adduser -u 10001 -G appgroup -s /sbin/nologin -D appuser && \
    mkdir -p /app && chown -R appuser:appgroup /app

WORKDIR /app

# Copy binary from builder
COPY --from=builder --chown=appuser:appgroup /bin/social-mcp-server /app/social-mcp-server

# Switch to unprivileged non-root user
USER 10001:10001

# Expose HTTP / MCP Server Port
EXPOSE 8080

# Configure Docker Healthcheck using minimal safe /health endpoint
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:8080/health || exit 1

# Entrypoint execution
ENTRYPOINT ["/app/social-mcp-server"]
