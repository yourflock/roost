FROM golang:1.24-alpine AS builder
RUN apk add --no-cache git

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY internal/ ./internal/
COPY services/billing/ ./services/billing/
COPY cmd/billing/ ./cmd/billing/

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" \
    -o /bin/billing ./cmd/billing/

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata wget
COPY --from=builder /bin/billing /bin/billing

ENV BILLING_PORT=8085
EXPOSE 8085

USER nobody
ENTRYPOINT ["/bin/billing"]
