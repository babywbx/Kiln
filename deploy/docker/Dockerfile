# syntax=docker/dockerfile:1

ARG FFMPEG_IMAGE=mwader/static-ffmpeg:8.1.2@sha256:33f770f812cbfc3de96c547157fc9faf8bd95a36481753439ffa761045167585
ARG ALPINE_IMAGE=alpine:3.24.1@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
ARG GO_IMAGE=golang:1.26.5-bookworm@sha256:1ecb7edf62a0408027bd5729dfd6b1b8766e578e8df93995b225dfd0944eb651

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build-base
ARG TARGETARCH
ARG TARGETVARIANT
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILT_AT=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .

FROM build-base AS build
ARG TARGETARCH
ARG TARGETVARIANT
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILT_AT=unknown
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    . ./deploy/docker/go-target-env.sh "$TARGETARCH" "$TARGETVARIANT" \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags="-s -w \
        -X github.com/babywbx/kiln/modules/version.Version=${VERSION} \
        -X github.com/babywbx/kiln/modules/version.Commit=${COMMIT} \
        -X github.com/babywbx/kiln/modules/version.BuiltAt=${BUILT_AT}" \
      -o /out/kiln ./apps/server \
 && go version -m /out/kiln > /out/go-build-info \
 && grep -Eq "build[[:space:]]+GOARCH=${GOARCH}$" /out/go-build-info \
 && if [ -n "${GOARM:-}" ]; then grep -Eq "build[[:space:]]+GOARM=${GOARM}$" /out/go-build-info; fi \
 && if [ -n "${GOAMD64:-}" ]; then grep -Eq "build[[:space:]]+GOAMD64=${GOAMD64}$" /out/go-build-info; fi \
 && if [ -n "${GOARM64:-}" ]; then grep -Eq "build[[:space:]]+GOARM64=${GOARM64}$" /out/go-build-info; fi

FROM build-base AS lite-build
ARG TARGETARCH
ARG TARGETVARIANT
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILT_AT=unknown
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    . ./deploy/docker/go-target-env.sh "$TARGETARCH" "$TARGETVARIANT" \
 && CGO_ENABLED=0 GOOS=linux go build -tags=lite -trimpath \
      -ldflags="-s -w -buildid= \
        -X github.com/babywbx/kiln/modules/version.Version=${VERSION} \
        -X github.com/babywbx/kiln/modules/version.Commit=${COMMIT} \
        -X github.com/babywbx/kiln/modules/version.BuiltAt=${BUILT_AT}" \
      -o /out/kiln ./apps/server \
 && go version -m /out/kiln > /out/go-build-info \
 && grep -Eq "build[[:space:]]+-tags=lite$" /out/go-build-info \
 && grep -Eq "build[[:space:]]+GOARCH=${GOARCH}$" /out/go-build-info \
 && if [ -n "${GOARM:-}" ]; then grep -Eq "build[[:space:]]+GOARM=${GOARM}$" /out/go-build-info; fi \
 && if [ -n "${GOAMD64:-}" ]; then grep -Eq "build[[:space:]]+GOAMD64=${GOAMD64}$" /out/go-build-info; fi \
 && if [ -n "${GOARM64:-}" ]; then grep -Eq "build[[:space:]]+GOARM64=${GOARM64}$" /out/go-build-info; fi

FROM ${ALPINE_IMAGE} AS runtime-base
ARG VERSION=dev
ARG COMMIT=unknown
ARG ALPINE_IMAGE
RUN apk add --no-cache ca-certificates tzdata \
 && delgroup ping \
 && addgroup -S -g 999 kiln \
 && adduser -S -D -H -u 999 -G kiln -h /var/lib/kiln -s /sbin/nologin kiln \
 && mkdir -p /var/lib/kiln/data /etc/kiln /usr/local/share/kiln \
 && chown -R kiln:kiln /var/lib/kiln /etc/kiln
COPY --from=build  /out/kiln /usr/local/bin/kiln
COPY --from=build  /out/go-build-info /usr/local/share/kiln/go-build-info
LABEL org.opencontainers.image.title="Kiln" \
      org.opencontainers.image.source="https://github.com/babywbx/kiln" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.licenses="AGPL-3.0-only" \
      io.kiln.base.source="${ALPINE_IMAGE}"
USER kiln
WORKDIR /var/lib/kiln
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
  CMD wget -q -O /dev/null --spider http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/kiln"]
CMD ["-config", "/etc/kiln/kiln.toml"]

FROM runtime-base AS core-verify
USER root
RUN test ! -e /usr/local/bin/ffmpeg \
 && /usr/local/bin/kiln -h >/dev/null 2>&1 \
 && printf 'variant=core\nffmpeg=absent\npackager_default=native\n' > /verified

FROM runtime-base AS core
ENV KILN_DEFAULT_PACKAGER_ENGINE=native \
    KILN_RUNTIME_VARIANT=core
LABEL org.opencontainers.image.description="Kiln with the native media engine and no bundled FFmpeg" \
      io.kiln.variant="core" \
      io.kiln.ffmpeg.available="false" \
      io.kiln.packager.default="native"
COPY --from=core-verify /verified /usr/local/share/kiln/build-verified

FROM ${FFMPEG_IMAGE} AS ffmpeg

FROM runtime-base AS full-runtime
USER root
COPY --from=ffmpeg /ffmpeg /usr/local/bin/ffmpeg

FROM full-runtime AS full-verify
COPY deploy/docker/verify-ffmpeg.sh deploy/docker/smoke.sh /usr/local/bin/
COPY testdata/cenc /opt/fixtures/cenc
RUN /usr/local/bin/verify-ffmpeg.sh /usr/local/bin/ffmpeg \
 && /usr/local/bin/smoke.sh /usr/local/bin/ffmpeg /opt/fixtures/cenc \
 && printf 'variant=full\nffmpeg=%s\npackager_default=auto\n' \
      "$(/usr/local/bin/ffmpeg -hide_banner -version | head -1)" > /verified

FROM full-runtime AS full
ARG FFMPEG_IMAGE
ENV KILN_DEFAULT_PACKAGER_ENGINE=auto \
    KILN_RUNTIME_VARIANT=full
LABEL org.opencontainers.image.description="Kiln with native media processing and bundled FFmpeg compatibility" \
      io.kiln.variant="full" \
      io.kiln.ffmpeg.available="true" \
      io.kiln.packager.default="auto" \
      io.kiln.ffmpeg.source="${FFMPEG_IMAGE}"
COPY --from=full-verify /verified /usr/local/share/kiln/build-verified
USER kiln

FROM ${ALPINE_IMAGE} AS lite-rootfs
RUN apk add --no-cache ca-certificates \
 && printf 'kiln:x:999:\n' > /etc/group \
 && printf 'kiln:x:999:999:Kiln:/var/lib/kiln:/sbin/nologin\n' > /etc/passwd \
 && mkdir -p /var/lib/kiln/data /etc/kiln \
 && chown -R 999:999 /var/lib/kiln /etc/kiln

FROM scratch AS lite
ARG VERSION=dev
ARG COMMIT=unknown
COPY --from=lite-rootfs /etc/passwd /etc/group /etc/
COPY --from=lite-rootfs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=lite-rootfs --chown=999:999 /var/lib/kiln /var/lib/kiln
COPY --from=lite-rootfs --chown=999:999 /etc/kiln /etc/kiln
COPY --from=lite-build /out/kiln /usr/local/bin/kiln
COPY --from=lite-build /out/go-build-info /usr/local/share/kiln/go-build-info
ENV SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt \
    KILN_DEFAULT_PACKAGER_ENGINE=native
LABEL org.opencontainers.image.title="Kiln Lite" \
      org.opencontainers.image.source="https://github.com/babywbx/kiln" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.licenses="AGPL-3.0-only" \
      org.opencontainers.image.description="Stateless Kiln with native media processing" \
      io.kiln.variant="lite" \
      io.kiln.ffmpeg.available="false" \
      io.kiln.database.available="false" \
      io.kiln.admin.available="false" \
      io.kiln.packager.default="native" \
      io.kiln.base.source="scratch"
USER 999:999
WORKDIR /var/lib/kiln
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
  CMD ["/usr/local/bin/kiln", "-healthcheck", "http://127.0.0.1:8080/healthz"]
ENTRYPOINT ["/usr/local/bin/kiln"]
CMD ["-config", "/etc/kiln/kiln.toml"]
