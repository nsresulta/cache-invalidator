FROM golang:1.13

WORKDIR /go/

ENV GOPATH /go/src
ENV GOBIN /go/bin
ENV NAMESPACE sites
ENV REDIS_HOST localhost
ENV REDIS_PORT 6379
ENV LOGLEVEL INFO
ENV AWS_ACCESS_KEY_ID SECRET
ENV AWS_SECRET_ACCESS_KEY SECRET
# By default REMOTE_REDIS_HOSTS should not be set
# ENV REMOTE_REDIS_HOSTS localhost
# By defualt WEBHOOKS_CONFIG should not be set
# ENV WEBHOOKS_CONFIG /config/webhook.json
COPY ./cmd ./cmd/
COPY ./go.mod ./go.mod
COPY ./go.sum ./go.sum

# Download all dependencies and generate the binary
RUN go mod download && \
    go install ./cmd/cache-invalidator && \
    chmod -R +x ./bin

CMD ["./bin/cache-invalidator"]
