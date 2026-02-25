FROM golang:1.22-alpine AS builder
RUN apk add --no-cache git

WORKDIR /app
COPY services/recommendations/go.mod services/recommendations/go.sum ./
RUN go mod download

COPY services/recommendations/ .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /bin/recommendations ./cmd/recommendations/

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /bin/recommendations /bin/recommendations

ENV RECO_PORT=8099

EXPOSE 8099

USER nobody
ENTRYPOINT ["/bin/recommendations"]
