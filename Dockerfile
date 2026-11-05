# syntax=docker/dockerfile:1.7
FROM --platform=$BUILDPLATFORM golang:1.26-alpine@sha256:a2012b4a1d1f9f7e9f44e0d39b805a901e856b1dd80b4e6ee4338db547050308 AS build
WORKDIR /source
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Dirty source is an explicit local rehearsal option; release builds use false.
ARG ALLOW_DIRTY=false
RUN if [ "$ALLOW_DIRTY" = true ]; then go run ./cmd/prepare-web -manifest web-artifact/manifest.json -output web/dist -allow-dirty; else go run ./cmd/prepare-web -manifest web-artifact/manifest.json -output web/dist; fi
RUN go test -tags=embed_web ./web
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -tags=embed_web -trimpath -ldflags="-s -w" -o /out/zennotes-server ./cmd/zennotes-server
# A scratch image has no /tmp, and an upload over 8 MiB spills to a temp file. COPY
# keeps a folder's mode only for what is inside the folder it copies, hence the parent.
RUN mkdir -p /out/root/tmp && chmod 1777 /out/root/tmp
FROM scratch
LABEL org.opencontainers.image.title="ZenNotes" \
      org.opencontainers.image.description="Self-hosted ZenNotes server with a pinned browser artifact." \
      org.opencontainers.image.source="https://github.com/ZenNotes/znserver"
COPY --from=build /out/zennotes-server /zennotes-server
COPY --from=build /out/root/ /
ENV ZENNOTES_BIND=0.0.0.0:7878 \
    ZENNOTES_CONFIG_PATH=/data/server.json \
    ZENNOTES_DEFAULT_VAULT_PATH=/workspace \
    ZENNOTES_BROWSE_ROOTS=/workspace
USER 65532:65532
EXPOSE 7878
VOLUME ["/workspace", "/data"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --start-interval=1s --retries=3 \
    CMD ["/zennotes-server", "healthcheck"]
ENTRYPOINT ["/zennotes-server"]
