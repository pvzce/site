ARG GO_VERSION=1.27.1
FROM golang:${GO_VERSION}-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
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
