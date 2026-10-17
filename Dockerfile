# syntax=docker/dockerfile:1
ARG GO_VERSION=1.27.2
FROM golang:${GO_VERSION} AS build

WORKDIR /src
COPY go.mod go.sum ./
# The optional combined CA bundle is mounted only for this command, never persisted.
# Restricted cloud environments may fetch an individual module from its official Git repository.
ARG DIRECT_MODULE_DOWNLOADS=""
RUN --mount=type=secret,id=proxy_ca \
    if [ -f /run/secrets/proxy_ca ]; then export SSL_CERT_FILE=/run/secrets/proxy_ca GIT_SSL_CAINFO=/run/secrets/proxy_ca; fi; \
    if [ -n "$DIRECT_MODULE_DOWNLOADS" ]; then GOPROXY=direct go mod download "$DIRECT_MODULE_DOWNLOADS"; fi && \
    go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -trimpath -ldflags="-s -w -X pvzce-site/internal/buildinfo.Version=${VERSION}" \
    -o /out/server ./cmd/server

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/server /server
COPY data/.gitkeep /data/.gitkeep
COPY configs/.gitkeep /configs/.gitkeep
COPY --chown=65532:65532 cache /cache
USER 65532:65532
ENV APP_ENV=production HTTP_ADDR=:8080
EXPOSE 8080
STOPSIGNAL SIGTERM
HEALTHCHECK --interval=20s --timeout=3s --start-period=5s --retries=3 \
    CMD ["/server", "healthcheck"]
ENTRYPOINT ["/server"]
