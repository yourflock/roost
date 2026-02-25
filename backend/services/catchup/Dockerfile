FROM golang:1.22-alpine AS builder
RUN apk add --no-cache git

WORKDIR /app
COPY services/catchup/go.mod services/catchup/go.sum ./
RUN go mod download

COPY services/catchup/ .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /bin/catchup ./cmd/catchup/

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata ffmpeg
COPY --from=builder /bin/catchup /bin/catchup

ENV CATCHUP_PORT=8098 \
    CATCHUP_RETENTION_DAYS=7

EXPOSE 8098

USER nobody
ENTRYPOINT ["/bin/catchup"]
