# AntBox Docker Image (T-7H.2.001)
# Multi-stage build for minimal production image.

# ---- Build stage ----
FROM golang:1.21-alpine AS builder

WORKDIR /build

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

# Cache deps
COPY go.mod go.sum ./
RUN go mod download

# Build binary
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.Version=${VERSION:-dev}" \
    -o /antbox \
    .

# ---- Production stage ----
FROM alpine:3.19

# DVB/V4L2 tools
RUN apk add --no-cache \
    ca-certificates \
    tzdata \
    ffmpeg \
    v4l-utils \
    libusb \
    && rm -rf /var/cache/apk/*

# Create non-root user
RUN addgroup -S antbox && adduser -S -G antbox antbox

COPY --from=builder /antbox /usr/local/bin/antbox

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD wget -qO- http://localhost:${ANTBOX_HTTP_PORT:-8089}/health || exit 1

USER antbox

EXPOSE 8089

ENTRYPOINT ["/usr/local/bin/antbox"]
