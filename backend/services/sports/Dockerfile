# Stage 1: build
# sports is a standalone Go module with its own go.mod/go.sum
FROM golang:1.22-alpine AS builder
RUN apk add --no-cache git

WORKDIR /app

# Copy the sports module files
COPY services/sports/go.mod services/sports/go.sum ./

# Cache dependencies
RUN go mod download

# Copy sports source
COPY services/sports/ .

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" \
    -o /bin/sports ./cmd/sports/

# Stage 2: minimal runtime
FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata wget

# Non-root user for security
RUN adduser -D -H -h /nonexistent appuser
USER appuser

COPY --from=builder /bin/sports /bin/sports

ENV SPORTS_PORT=8102
EXPOSE 8102

HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
  CMD wget -qO- http://localhost:${SPORTS_PORT}/health || exit 1

ENTRYPOINT ["/bin/sports"]
