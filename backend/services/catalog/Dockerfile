FROM golang:1.22-alpine AS builder
RUN apk add --no-cache git

WORKDIR /app
# Copy root go.mod (shared module)
COPY go.mod go.sum ./
RUN go mod download

# Copy all source needed by this service
COPY internal/ ./internal/
COPY services/catalog/ ./services/catalog/

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" \
    -o /bin/catalog ./services/catalog/cmd/catalog/

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata wget
COPY --from=builder /bin/catalog /bin/catalog

ENV CATALOG_PORT=8095
EXPOSE 8095

USER nobody
ENTRYPOINT ["/bin/catalog"]
