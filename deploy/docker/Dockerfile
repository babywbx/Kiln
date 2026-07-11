# syntax=docker/dockerfile:1

ARG FFMPEG_IMAGE=mwader/static-ffmpeg:8.1.2@sha256:33f770f812cbfc3de96c547157fc9faf8bd95a36481753439ffa761045167585

FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS build
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILT_AT=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH \
    go build -trimpath \
      -ldflags="-s -w \
        -X github.com/babywbx/kiln/modules/version.Version=${VERSION} \
        -X github.com/babywbx/kiln/modules/version.Commit=${COMMIT} \
        -X github.com/babywbx/kiln/modules/version.BuiltAt=${BUILT_AT}" \
      -o /out/kiln ./apps/server

FROM ${FFMPEG_IMAGE} AS ffmpeg

FROM alpine:3.22 AS runtime
RUN apk add --no-cache ca-certificates tzdata \
 && delgroup ping \
 && addgroup -S -g 999 kiln \
 && adduser -S -D -H -u 999 -G kiln -h /var/lib/kiln -s /sbin/nologin kiln \
 && mkdir -p /var/lib/kiln/data /etc/kiln /usr/local/share/kiln \
 && chown -R kiln:kiln /var/lib/kiln /etc/kiln
COPY --from=ffmpeg /ffmpeg   /usr/local/bin/ffmpeg
COPY --from=build  /out/kiln /usr/local/bin/kiln

FROM runtime AS verify
COPY deploy/docker/verify-ffmpeg.sh deploy/docker/smoke.sh /usr/local/bin/
COPY testdata/cenc /opt/fixtures/cenc
RUN /usr/local/bin/verify-ffmpeg.sh /usr/local/bin/ffmpeg \
 && /usr/local/bin/smoke.sh /usr/local/bin/ffmpeg /opt/fixtures/cenc \
 && printf 'ffmpeg=%s\n' "$(/usr/local/bin/ffmpeg -hide_banner -version | head -1)" > /verified

FROM runtime AS final
ARG VERSION=dev
ARG COMMIT=unknown
ARG FFMPEG_IMAGE
LABEL org.opencontainers.image.title="Kiln" \
      org.opencontainers.image.source="https://github.com/babywbx/kiln" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.licenses="AGPL-3.0-only" \
      io.kiln.ffmpeg.source="${FFMPEG_IMAGE}"
COPY --from=verify /verified /usr/local/share/kiln/build-verified
USER kiln
WORKDIR /var/lib/kiln
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/kiln"]
CMD ["-config", "/etc/kiln/kiln.toml"]
