# worldland-node Dockerfile
# Builds GPU node service for individual provider scenario

# Use standard golang image (Debian-based) for CGO/NVML compatibility
FROM golang:1.24 AS builder

WORKDIR /app

# Copy go.mod and go.sum first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the node binary with CGO enabled (required for NVML)
RUN CGO_ENABLED=1 GOOS=linux go build -o /worldland-node ./cmd/node

# Runtime stage - use Debian slim for glibc compatibility with NVML
FROM debian:bookworm-slim

WORKDIR /app

# Install runtime dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    docker.io \
    && rm -rf /var/lib/apt/lists/*

# Copy binary from builder
COPY --from=builder /worldland-node /app/worldland-node

# Create non-root user (optional - but we need Docker access so may run as root)
# RUN adduser -D -g '' appuser
# USER appuser

# Expose mTLS API port and health port
EXPOSE 8444

# Default command
ENTRYPOINT ["/app/worldland-node"]
