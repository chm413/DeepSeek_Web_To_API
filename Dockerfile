FROM node:24 AS webui-builder

WORKDIR /app/webui
COPY webui/package.json webui/package-lock.json ./
RUN npm ci
COPY config.example.json /app/config.example.json
COPY webui ./
RUN npm run build

FROM golang:1.26 AS go-builder
WORKDIR /app
ARG TARGETOS
ARG TARGETARCH
ARG BUILD_VERSION
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN set -eux; \
    GOOS="${TARGETOS:-$(go env GOOS)}"; \
    GOARCH="${TARGETARCH:-$(go env GOARCH)}"; \
    BUILD_VERSION_RESOLVED="${BUILD_VERSION:-}"; \
    if [ -z "${BUILD_VERSION_RESOLVED}" ] && [ -f VERSION ]; then BUILD_VERSION_RESOLVED="$(cat VERSION | tr -d "[:space:]")"; fi; \
    CGO_ENABLED=0 GOOS="${GOOS}" GOARCH="${GOARCH}" go build -buildvcs=false -ldflags="-s -w -X DeepSeek_Web_To_API/internal/version.BuildVersion=${BUILD_VERSION_RESOLVED}" -o /out/deepseek-web-to-api ./cmd/DeepSeek_Web_To_API

FROM busybox:1.36.1-musl AS busybox-tools

FROM debian:bookworm-slim AS runtime-base
WORKDIR /app
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && groupadd -r deepseek-web-to-api && useradd -r -g deepseek-web-to-api -d /app -s /sbin/nologin deepseek-web-to-api \
    && mkdir -p /app/data && chown -R deepseek-web-to-api:deepseek-web-to-api /app \
    && rm -rf /var/lib/apt/lists/*
COPY --from=busybox-tools /bin/busybox /usr/local/bin/busybox
COPY docker/entrypoint.sh /usr/local/bin/deepseek-web-to-api-entrypoint
COPY docker/migrate-legacy-config.sh /usr/local/bin/deepseek-web-to-api-migrate-legacy-config
COPY VERSION /tmp/deepseek-web-to-api-version
RUN set -eux; \
    version="$(tr -d '[:space:]' < /tmp/deepseek-web-to-api-version)"; \
    case "${version}" in [0-9]*.[0-9]*.[0-9]*) ;; *) exit 1 ;; esac; \
    mkdir -p /usr/local/share/deepseek-web-to-api; \
    printf 'v%s\n' "${version}" > /usr/local/share/deepseek-web-to-api/VERSION; \
    rm -f /tmp/deepseek-web-to-api-version; \
    chmod 0555 /usr/local/bin/deepseek-web-to-api-entrypoint /usr/local/bin/deepseek-web-to-api-migrate-legacy-config
EXPOSE 5001
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD ["/usr/local/bin/busybox", "wget", "-q", "-O", "/dev/null", "http://127.0.0.1:5001/healthz"]
ENTRYPOINT ["/usr/local/bin/deepseek-web-to-api-entrypoint"]
CMD ["/usr/local/bin/deepseek-web-to-api"]

FROM runtime-base AS runtime-from-source
COPY --from=go-builder /out/deepseek-web-to-api /usr/local/bin/deepseek-web-to-api

COPY --from=go-builder --chown=deepseek-web-to-api:deepseek-web-to-api /app/config.example.json /app/config.example.json
COPY --from=webui-builder --chown=deepseek-web-to-api:deepseek-web-to-api /app/static/admin /app/static/admin
USER deepseek-web-to-api

FROM busybox-tools AS dist-extract
ARG TARGETARCH
COPY dist/docker-input/linux_amd64.tar.gz /tmp/deepseek-web-to-api_linux_amd64.tar.gz
COPY dist/docker-input/linux_arm64.tar.gz /tmp/deepseek-web-to-api_linux_arm64.tar.gz
RUN set -eux; \
    case "${TARGETARCH}" in \
      amd64) ARCHIVE="/tmp/deepseek-web-to-api_linux_amd64.tar.gz" ;; \
      arm64) ARCHIVE="/tmp/deepseek-web-to-api_linux_arm64.tar.gz" ;; \
      *) echo "unsupported TARGETARCH: ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    tar -xzf "${ARCHIVE}" -C /tmp; \
    PKG_DIR="$(find /tmp -maxdepth 1 -type d -name "deepseek-web-to-api_*_linux_${TARGETARCH}" | head -n1)"; \
    test -n "${PKG_DIR}"; \
    mkdir -p /out/static; \
    cp "${PKG_DIR}/deepseek-web-to-api" /out/deepseek-web-to-api; \
    cp "${PKG_DIR}/config.example.json" /out/config.example.json; \
    cp -R "${PKG_DIR}/static/admin" /out/static/admin

FROM runtime-base AS runtime-from-dist
COPY --from=dist-extract /out/deepseek-web-to-api /usr/local/bin/deepseek-web-to-api

COPY --from=dist-extract --chown=deepseek-web-to-api:deepseek-web-to-api /out/config.example.json /app/config.example.json
COPY --from=dist-extract --chown=deepseek-web-to-api:deepseek-web-to-api /out/static/admin /app/static/admin
USER deepseek-web-to-api

FROM runtime-from-source AS final
