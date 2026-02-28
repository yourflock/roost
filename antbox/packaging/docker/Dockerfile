# Stage 1: Build
FROM golang:1.22-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags "-X antbox/daemon.Version=1.0.0 -s -w" -o antboxd ./cmd/antboxd 2>/dev/null || \
    CGO_ENABLED=0 GOOS=linux go build -ldflags "-X antbox/daemon.Version=1.0.0 -s -w" -o antboxd .

# Stage 2: Runtime
FROM alpine:3.19
RUN apk add --no-cache tzdata ca-certificates
RUN adduser -D -s /sbin/nologin antbox
COPY --from=builder /build/antboxd /usr/local/bin/antboxd
COPY configs/antbox.yaml.example /etc/antbox/antbox.yaml
RUN chown -R antbox:antbox /etc/antbox
USER antbox
EXPOSE 50051 8087
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://localhost:8087/health || exit 1
CMD ["/usr/local/bin/antboxd", "--config", "/etc/antbox/antbox.yaml"]
