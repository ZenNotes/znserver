# syntax=docker/dockerfile:1.7
FROM --platform=$BUILDPLATFORM golang:1.26-alpine@sha256:3ad57304ad93bbec8548a0437ad9e06a455660655d9af011d58b993f6f615648 AS build
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
FROM scratch
LABEL org.opencontainers.image.title="ZenNotes" \
      org.opencontainers.image.description="Self-hosted ZenNotes server with a pinned browser artifact." \
      org.opencontainers.image.source="https://github.com/ZenNotes/znserver"
COPY --from=build /out/zennotes-server /zennotes-server
ENV ZENNOTES_BIND=0.0.0.0:7878 \
    ZENNOTES_CONFIG_PATH=/data/server.json \
    ZENNOTES_DEFAULT_VAULT_PATH=/workspace \
    ZENNOTES_BROWSE_ROOTS=/workspace
USER 65532:65532
EXPOSE 7878
VOLUME ["/workspace", "/data"]
ENTRYPOINT ["/zennotes-server"]
