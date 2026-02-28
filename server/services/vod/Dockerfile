FROM golang:1.22-alpine AS builder
RUN apk add --no-cache git

WORKDIR /app
COPY services/vod/go.mod services/vod/go.sum ./
RUN go mod download

COPY services/vod/ .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /bin/vod ./cmd/vod/

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /bin/vod /bin/vod

ENV VOD_PORT=8097
EXPOSE 8097

USER nobody
ENTRYPOINT ["/bin/vod"]
