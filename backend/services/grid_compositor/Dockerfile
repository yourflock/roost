FROM golang:1.22-alpine AS builder
RUN apk add --no-cache git

WORKDIR /app
COPY services/grid_compositor/go.mod services/grid_compositor/go.sum ./
RUN go mod download

COPY services/grid_compositor/ .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /bin/grid_compositor ./cmd/grid_compositor/

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata ffmpeg
COPY --from=builder /bin/grid_compositor /bin/grid_compositor

ENV COMPOSITOR_PORT=8100 \
    SEGMENT_DIR=/var/roost/segments \
    COMPOSITOR_OUTPUT_DIR=/var/roost/compositor

EXPOSE 8100

USER nobody
ENTRYPOINT ["/bin/grid_compositor"]
