FROM golang:1.24-alpine AS builder
RUN apk add --no-cache git

# Preserve backend workspace layout so `replace ../../` in services/relay/go.mod
# resolves to /app (the root module). Docker context = backend/.
WORKDIR /app

# Root module (replace target for relay's go.mod)
COPY go.mod go.sum ./

# Relay module in correct relative path
COPY services/relay/go.mod ./services/relay/go.mod
COPY services/relay/go.sum ./services/relay/go.sum

# Download dependencies from relay's perspective
WORKDIR /app/services/relay
RUN go mod download

# Copy all source (includes internal/ packages relay imports from root)
WORKDIR /app
COPY . .

# Build
WORKDIR /app/services/relay
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /bin/relay ./cmd/relay/

FROM alpine:3.21 AS final
RUN apk add --no-cache ca-certificates tzdata wget
COPY --from=builder /bin/relay /bin/relay

ENV RELAY_PORT=8090
EXPOSE 8090

USER nobody
ENTRYPOINT ["/bin/relay"]
