# syntax=docker/dockerfile:1

FROM golang:1.26.6-alpine3.23 AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=linux go build \
    -buildvcs=false \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/routeforge \
    ./cmd/routeforge

FROM alpine:3.21.6

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder --chown=65532:65532 /out/routeforge /usr/local/bin/routeforge

USER 65532:65532

EXPOSE 8080 9090

ENTRYPOINT ["/usr/local/bin/routeforge"]
