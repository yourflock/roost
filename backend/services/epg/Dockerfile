FROM golang:1.22-alpine AS builder
RUN apk add --no-cache git

WORKDIR /app
COPY services/epg/go.mod services/epg/go.sum ./
RUN go mod download

COPY services/epg/ .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /bin/epg ./cmd/epg/

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata wget
COPY --from=builder /bin/epg /bin/epg

ENV EPG_PORT=8096
EXPOSE 8096

USER nobody
ENTRYPOINT ["/bin/epg"]
