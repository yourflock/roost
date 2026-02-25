# Stage 1: build
FROM golang:1.22-alpine AS builder
RUN apk add --no-cache git ffmpeg

WORKDIR /app
COPY services/ingest/go.mod services/ingest/go.sum ./
RUN go mod download

COPY services/ingest/ .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /bin/ingest ./cmd/ingest/

# Stage 2: runtime
FROM alpine:3.19
RUN apk add --no-cache ffmpeg ca-certificates tzdata wget
COPY --from=builder /bin/ingest /bin/ingest

ENV INGEST_PORT=8094
EXPOSE 8094

USER nobody
ENTRYPOINT ["/bin/ingest"]
