FROM golang:1.24-alpine AS builder
RUN apk add --no-cache git

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY internal/ ./internal/
COPY services/owl_api/ ./services/owl_api/

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" \
    -o /bin/owl_api ./services/owl_api/cmd/owl_api/

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata wget
COPY --from=builder /bin/owl_api /bin/owl_api

ENV OWL_API_PORT=8091
EXPOSE 8091

USER nobody
ENTRYPOINT ["/bin/owl_api"]
