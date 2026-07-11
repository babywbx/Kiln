# syntax=docker/dockerfile:1

FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/kiln ./apps/server

FROM debian:bookworm-slim
RUN apt-get update \
  && apt-get install -y --no-install-recommends ca-certificates ffmpeg \
  && rm -rf /var/lib/apt/lists/* \
  && useradd --system --home /var/lib/kiln --shell /usr/sbin/nologin kiln \
  && mkdir -p /var/lib/kiln/data /etc/kiln \
  && chown -R kiln:kiln /var/lib/kiln /etc/kiln
COPY --from=build /out/kiln /usr/local/bin/kiln
USER kiln
WORKDIR /var/lib/kiln
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/kiln"]
CMD ["-config", "/etc/kiln/kiln.toml"]
