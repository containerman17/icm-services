# Build the Go application
FROM golang:1.23 AS builder
WORKDIR /app
# Cache Go modules using volume
ENV GOPATH /go
COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOCACHE=/root/.cache/go-build \
    go build -o /usr/bin/signature-aggregator ./signature-aggregator/main/main.go

# Create final image
FROM debian:12-slim
RUN apt update && apt --yes install ca-certificates

COPY --from=builder /usr/bin/signature-aggregator /usr/bin/signature-aggregator
COPY signature-aggregator/sample-signature-aggregator-config.json /etc/signature-aggregator/config.json

CMD ["--config-file", "/etc/signature-aggregator/config.json"]
ENTRYPOINT [ "/usr/bin/signature-aggregator" ]
