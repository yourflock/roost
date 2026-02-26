FROM golang:1.24-alpine AS builder
RUN apk add --no-cache git

# Preserve backend workspace layout so `replace ../../` in services/epg/go.mod
# resolves to /app (the root module). Docker context = backend/.
WORKDIR /app

# Root module (replace target for epg's go.mod)
COPY go.mod go.sum ./

# EPG module in correct relative path
COPY services/epg/go.mod ./services/epg/go.mod
COPY services/epg/go.sum ./services/epg/go.sum

# Download dependencies from epg's perspective
WORKDIR /app/services/epg
RUN go mod download

# Copy all source
WORKDIR /app
COPY . .

# Build
WORKDIR /app/services/epg
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /bin/epg ./cmd/epg/

FROM alpine:3.19 AS final
RUN apk add --no-cache ca-certificates tzdata wget
COPY --from=builder /bin/epg /bin/epg

ENV EPG_PORT=8096
EXPOSE 8096

USER nobody
ENTRYPOINT ["/bin/epg"]
