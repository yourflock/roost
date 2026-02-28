# Roost — Multi-stage Dockerfile for self-hosted deployment.
# P19.1.001: Multi-Stage Dockerfile for Roost Binary
#
# Stage 1: Build the Go binary.
# Stage 2: Minimal runtime image (distroless/static, nonroot user).
#
# Build (from repo root):
#   docker build -f server/Dockerfile -t roost:latest .
#
# Run:
#   docker run -p 8080:8080 --env-file server/.env roost:latest

# ── Stage 1: Builder ──────────────────────────────────────────────────────────
FROM golang:1.24-alpine AS builder

# Install dependencies for CGO=0 builds.
RUN apk add --no-cache ca-certificates git tzdata

WORKDIR /build

# Copy module files first for layer caching.
COPY server/go.mod server/go.sum ./server/

# Download dependencies (cached layer).
RUN cd server && go mod download

# Copy all source.
COPY server/ ./server/

# Build the binary. CGO_ENABLED=0 for a fully static binary.
# -s -w strips debug info (reduces size). Embed version from VERSION file if present.
ARG VERSION=dev
RUN cd server && CGO_ENABLED=0 GOWORK=off GOOS=linux GOARCH=amd64 go build \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -trimpath \
    -o /bin/roost \
    ./cmd/api/

# ── Stage 2: Runtime ──────────────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot

# Copy binary from builder.
COPY --from=builder /bin/roost /roost

# Copy CA certificates for HTTPS calls (Stripe, external APIs, etc.).
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy timezone data for proper time handling.
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

EXPOSE 8080

# Run as nonroot user (UID 65532 in distroless:nonroot).
USER nonroot:nonroot

LABEL org.opencontainers.image.title="Roost"
LABEL org.opencontainers.image.description="Open-source universal media backend for Owl"
LABEL org.opencontainers.image.url="https://github.com/yourflock/roost"
LABEL org.opencontainers.image.licenses="MIT"

ENTRYPOINT ["/roost"]
