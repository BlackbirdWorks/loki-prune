# Build stage
FROM golang:1.27-alpine AS builder

ENV GOTOOLCHAIN=auto

RUN apk add --no-cache ca-certificates

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 go build \
    -tags 'netgo osusergo static_build' \
    -trimpath \
    -ldflags="-w -s -extldflags '-static -fno-PIC'" \
    -o loki-prune .

# Final stage
FROM scratch

WORKDIR /app

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /app/loki-prune .

LABEL org.opencontainers.image.source="https://github.com/blackbirdworks/loki-prune"
LABEL org.opencontainers.image.description="Automated storage-quota rotator and compression engine for Grafana Loki"

USER 65532:65532

ENTRYPOINT ["./loki-prune"]
CMD ["--config", "/etc/loki-prune/config.yaml"]
