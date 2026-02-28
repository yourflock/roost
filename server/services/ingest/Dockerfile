# Stage 1: build
FROM golang:1.24-alpine AS builder
RUN apk add --no-cache git ffmpeg

# Preserve backend workspace layout so replace directives in services/ingest/go.mod
# resolve correctly (replace ../../ = root, replace ../relay = relay module).
# Docker context = backend/.
WORKDIR /app

# Root module (replace target)
COPY go.mod go.sum ./

# Relay module (replace target for ingest's ../relay directive)
COPY services/relay/go.mod ./services/relay/go.mod
COPY services/relay/go.sum ./services/relay/go.sum

# Ingest module in correct relative path
COPY services/ingest/go.mod ./services/ingest/go.mod
COPY services/ingest/go.sum ./services/ingest/go.sum

# Download dependencies from ingest's perspective
WORKDIR /app/services/ingest
RUN go mod download

# Copy all source
WORKDIR /app
COPY . .

# Build
WORKDIR /app/services/ingest
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /bin/ingest ./cmd/ingest/

# Stage 2: runtime
FROM alpine:3.19 AS final
RUN apk add --no-cache ffmpeg ca-certificates tzdata wget
COPY --from=builder /bin/ingest /bin/ingest

ENV INGEST_PORT=8094
EXPOSE 8094

USER nobody
ENTRYPOINT ["/bin/ingest"]
