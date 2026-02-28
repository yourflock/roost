FROM golang:1.22-alpine AS builder
RUN apk add --no-cache git

WORKDIR /app
COPY services/dvr/go.mod services/dvr/go.sum ./
RUN go mod download

COPY services/dvr/ .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /bin/dvr ./cmd/dvr/

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata ffmpeg
COPY --from=builder /bin/dvr /bin/dvr

ENV DVR_PORT=8101 \
    SEGMENT_DIR=/var/roost/segments \
    DVR_SCRATCH_DIR=/var/roost/dvr/scratch \
    DVR_STORAGE_DIR=/var/roost/dvr/storage

EXPOSE 8101

USER nobody
ENTRYPOINT ["/bin/dvr"]
