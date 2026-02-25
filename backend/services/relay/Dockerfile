FROM golang:1.22-alpine AS builder
RUN apk add --no-cache git

WORKDIR /app
COPY services/relay/go.mod services/relay/go.sum ./
RUN go mod download

COPY services/relay/ .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /bin/relay ./cmd/relay/

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata wget
COPY --from=builder /bin/relay /bin/relay

ENV RELAY_PORT=8090
EXPOSE 8090

USER nobody
ENTRYPOINT ["/bin/relay"]
