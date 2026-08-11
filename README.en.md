<div align="center"><a name="readme-top"></a>

<img src="./apps/docs/public/icon.webp" alt="Kiln" width="152" height="152" />

# Kiln

A lightweight streaming aggregation gateway that turns self-hosted HLS and DASH<br/>
channels into one authenticated M3U playlist and on-demand HLS output.

[简体中文](./README.md) · [Documentation][docs-link] · [Report Issue][github-issues-link] · [Changelog][github-release-link]

<!-- SHIELD GROUP -->

[![][github-stars-shield]][github-stars-link]
[![][github-forks-shield]][github-forks-link]
[![][github-issues-shield]][github-issues-link]
[![][github-license-shield]][github-license-link]<br/>
[![][github-contributors-shield]][github-contributors-link]
[![][github-lastcommit-shield]][github-lastcommit-link]
[![][go-version-shield]][go-version-link]

</div>

<details>
<summary><kbd>Table of Contents</kbd></summary>

#### TOC

- [📋 Overview](#-overview)
- [✨ Features](#-features)
- [🚀 Quick Start](#-quick-start)
- [📦 Install Script](#-install-script)
- [🐳 Docker](#-docker)
- [🪟 Windows Service](#-windows-service)
- [⚙️ Configuration](#️-configuration)
- [🌐 Environment Variables](#-environment-variables)
- [🔌 API](#-api)
- [🧪 Development](#-development)
- [📁 Project Structure](#-project-structure)
- [🤝 Contributing](#-contributing)
- [📝 License](#-license)

####

<br/>

</details>

## 📋 Overview

This project solves one problem: self-hosted channels come in mixed formats, carry no authentication, and keep pulling upstream even when nobody is watching. Kiln sits between the origins and your players and collapses them into a single entry point.

```text
Self-hosted origins (HLS / DASH)  →  Kiln  →  Players / IPTV clients
                                       │
                                       ├─ Auth: password, session JWT, API token, playback key
                                       ├─ Pull: starts on demand, reclaims when idle
                                       ├─ Media: local decryption, native repackaging to HLS
                                       └─ Serve: M3U playlist, EPG, admin console
```

DASH decryption and repackaging are implemented natively in Go, so FFmpeg is not required. The whole service is a single binary; the smallest image is 3.8 MB and plays fine inside a 64 MiB container.

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## ✨ Features

| Feature | Description |
| --- | --- |
| Native media pipeline | Same-origin HLS segment proxy; DASH decrypted locally with `kid:key` and repackaged to HLS, no FFmpeg anywhere in the path |
| Low latency and multi-track | LL-HLS (CMAF parts, delta playlists, blocking reload), ABR, multiple audio tracks, TTML→WebVTT subtitles |
| On-demand pulling | `on_demand` reclaims the upstream connection when nobody is watching; `autostart` pre-warms frequently used channels |
| Self-healing | Media stall detection, exponential backoff restarts, publication generation isolation — players switch generations without a manual refresh |
| Full authentication | bcrypt passwords, Ed25519 session JWTs, admin API tokens shown exactly once, path-based playback keys |
| Distribution and audit | Playback keys scoped to a channel subset, bulk M3U import and export, playback access logs and API token audit trails |
| Playlist and EPG | Scoped M3U generation with automatically linked XMLTV, plus built-in logo source candidates |
| Outbound proxying | Route HTTP / SOCKS by host or channel; the channel editor can create and test a route inline |
| Admin console | Responsive web UI, channel pre-warming and preview, Pinyin / Jyutping search that bridges simplified and traditional forms, compressed static assets |
| Resource adaptation | Tightens memory budgets and concurrency from the container's real memory and CPU — scales down only, never up |
| Cross-platform | A single binary for Linux, macOS, and Windows, with built-in Windows service installation and restart policy |
| Observability | `/v1/status`, Prometheus `/metrics`, optional OTLP traces, `/healthz` and `/readyz` |

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🚀 Quick Start

This is the shortest path; see the [documentation site][docs-link] for full configuration, deployment options and API details.

Requires Go 1.26+. Native DASH needs no ffmpeg; only the compatibility fallback does.

```bash
git clone https://github.com/babywbx/Kiln.git
cd Kiln
go run ./apps/server -config configs/examples/kiln.toml
```

The server listens on `0.0.0.0:8080` by default, with the sample account `admin` / `admin` and the admin UI at `/admin`.

Log in once and everything else follows from the token:

```bash
TOKEN=$(curl -s http://127.0.0.1:8080/v1/auth/login \
  -H 'content-type: application/json' \
  -d '{"username":"admin","password":"admin"}' | jq -r .token)

curl -s http://127.0.0.1:8080/v1/channels -H "authorization: Bearer $TOKEN" | jq
curl -s http://127.0.0.1:8080/v1/playlist.m3u -H "authorization: Bearer $TOKEN"
curl -s "http://127.0.0.1:8080/v1/play/hls-demo/index.m3u8?token=$TOKEN"
```

Generate production credentials:

```bash
go run scripts/hash-password.go 'your-password'
go run scripts/gen-jwt-keys.go ./secrets   # writes ed25519.pem / ed25519.pub.pem
```

> \[!TIP\]
> Keys are optional to start: the process generates an Ed25519 key pair under `{data_dir}/auth/` with `0600` permissions on the private key.

For scripts and long-lived CLI access, create a dedicated token under **Settings → Admin API Tokens**. The plaintext is shown once and only its SHA-256 digest is stored. Each token can be granted `read`, `write`, `delete`, and `refresh` scopes independently, with an expiry, rotation, or revocation. API tokens cannot change login credentials or manage other tokens.

```bash
curl -s http://127.0.0.1:8080/v1/admin/channels \
  -H "authorization: Bearer kiln_v1_..." | jq
```

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 📦 Install Script

One command installs it on Linux and macOS; run it again to upgrade:

```bash
curl -fsSL https://raw.githubusercontent.com/babywbx/Kiln/main/install.sh | sh
```

The script does four things: detect your platform, pick a working download source, verify `SHA256SUMS`, and swap the binary atomically. No sudo needed by default, and it shows you the full plan before touching anything.

<details>
<summary><kbd>More options</kbd></summary>

<br/>

| Option | Description |
| --- | --- |
| `--yes` | silent install |
| `--version <v>` | pin a version |
| `--lite` | lite variant (Linux only) |
| `--dir <path>` | custom install directory |
| `--mirror <base>` | set a download mirror |
| `--service` | register a systemd service with autostart (root) |
| `--uninstall` | uninstall |
| `--dry-run` | preview and simulate every step, write nothing |

Install as a systemd service with autostart:

```bash
curl -fsSL https://raw.githubusercontent.com/babywbx/Kiln/main/install.sh -o /tmp/kiln-install.sh
sudo sh /tmp/kiln-install.sh --yes --service
```

</details>

On Windows, download the zip from [Releases][github-release-link] and follow the [Windows Service](#-windows-service) section.

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🐳 Docker

Three images with explicit boundaries, sharing one configuration model and the same native media modules:

| Image | Capabilities | Default packager | FFmpeg | Purpose |
| --- | --- | --- | --- | --- |
| `kiln:lite` | Config, login, M3U, playback | `native` | Not included | Minimal scratch runtime, 3.8 MB, no database |
| `kiln:core` | Full | `native` | Not included | Complete management and observability, natively only |
| `kiln:full` | Full | `auto` | Bundled, 9.0 | Native first, falls back when needed |
| `kiln:latest` | Full | `auto` | Bundled, 9.0 | Alias for `full` |

```bash
docker build --target full -f deploy/docker/Dockerfile -t kiln:full .

docker run --rm -p 8080:8080 \
  -v "$PWD/deploy/docker/kiln.docker.toml.example:/etc/kiln/kiln.toml:ro" \
  -v "$PWD/configs/examples/kiln.keys:/etc/kiln/kiln.keys:ro" \
  -v kiln-data:/var/lib/kiln/data \
  kiln:full
```

Lite targets fixed-configuration, low-resource playback nodes and runs fully read-only:

```bash
docker run --rm -p 8080:8080 --read-only \
  --cap-drop=ALL --security-opt=no-new-privileges \
  -v "$PWD/deploy/docker/lite.docker.toml.example:/etc/kiln/kiln.toml:ro" \
  -v "$PWD/configs/examples/kiln.keys:/etc/kiln/kiln.keys:ro" \
  -v kiln-lite-data:/var/lib/kiln \
  kiln:lite
```

It creates no SQLite database; `data_dir` only holds the auto-generated login keys and transient media files. The public surface is limited to `/healthz`, `/readyz`, `/v1/auth/login`, `/v1/playlist.m3u`, and `/v1/play/*`. An `auto`/`ffmpeg` packager, EPG, OTLP, or pprof in the config is rejected at startup rather than silently ignored.

> \[!NOTE\]
> Image defaults apply only when `[packager].engine` is absent from the config. An explicit `auto`, `native`, or `ffmpeg` always wins, so the same config never changes behavior because of an image tag.

`core` builds for `linux/amd64`, `linux/arm64`, `linux/arm/v7`, and `linux/arm/v6`. Capability boundaries, image sizes, and constrained-container measurements are documented in [Lite, Core and Full performance comparison][variant-doc], and `deploy/docker/compose.example.yaml` provides a Compose template.

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🪟 Windows Service

On Windows the built-in service commands run Kiln in the background, with no extra supervisor. Install and uninstall need an elevated terminal.

```powershell
kiln.exe service install -config C:\kiln\kiln.toml
kiln.exe service start
kiln.exe service status
kiln.exe service stop
kiln.exe service uninstall
```

`-name` picks the service name (default `Kiln`) so several instances can coexist on one host, and `-display` sets the display name. The service is installed as automatic-start with a three-step restart policy (5s, 15s, 60s).

The SCM starts processes in `system32`, so Kiln switches the working directory to the folder holding the config. Relative paths such as `data_dir = "./data"` therefore still resolve against the config file. Installation records the absolute config path, so moving the binary or the config means reinstalling the service.

Standard output is discarded under the SCM, so logs go to `kiln.log` next to the config. Once it passes 16 MB it is rotated to `kiln.log.1` on the next start.

> \[!NOTE\]
> The Windows distribution ships without ffmpeg. With `[packager].engine` set to `auto` the native engine is selected automatically; install ffmpeg and add it to `PATH` if the compatibility fallback is genuinely needed.

Serving beyond localhost needs an inbound rule, since the Windows firewall blocks it by default:

```powershell
New-NetFirewallRule -DisplayName "Kiln" -Direction Inbound -LocalPort 8080 -Protocol TCP -Action Allow
```

Uninstalling only removes the service registration; `data_dir`, logs, and the config are left in place.

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## ⚙️ Configuration

Examples live in `configs/examples/kiln.toml` and `kiln.jsonc` — the two formats are equivalent. Local private config belongs in `configs/local.toml`, which is gitignored.

| Setting | Description |
| --- | --- |
| `upstreams[].base_url` | Points at the origin; channels reference it via `upstream` + `path` |
| `upstreams[].upgrade_insecure_redirects` | Upgrades the scheme when an upstream redirects to `http` on an https-only origin; the same channel-level field opts in per channel, and only public hosts on default ports are upgraded |
| `[packager].engine` | `native` runs without ffmpeg entirely; `auto` prefers native and falls back when required |
| `[packager].keys_file` | Global `kid:key` catalog, one pair per line; relative paths resolve against `kiln.toml` |
| `[packager].ll_hls` | Enables CMAF parts, delta playlists, and blocking reload; `part_target_ms` sets the part duration |
| `[packager].inflight_bytes` | Cross-channel segment memory budget — the trade-off between peak memory and 4K cold-start latency |
| `[ffmpeg].mode` | `native` executes the local binary; `docker` lets Kiln launch a given image, no wrapper needed |
| `[auth]` | Ed25519 session JWTs, injectable via `token_private_key_file` or environment variables |
| `[epg]` | Disk, memory, or no cache; built-in sources ship disabled and can be routed per source |
| `[egress]` | Default outbound policy and host-based routing rules; `[[proxies]]` defines reusable routes |
| `[observe]` | `otlp_endpoint` enables OTLP/HTTP trace export; raw URLs, tokens, and query strings never enter spans |
| `[debug.pprof]` | Diagnostics only; must bind a loopback IP on a separate port and mux |

The key file is fully validated once at startup. Changes require a restart, and keys never appear in the admin API. Per-channel `keys` fields have been removed.

### Resource adaptation

`server.resource_mode` takes exactly three values: the recommended `auto`, the forced low-resource `constrained`, and `performance`, which opts out entirely. `auto` picks an internal profile from effective memory, then applies a CPU ceiling independently, and **only scales down — it never raises a lower value you configured**.

| Startup log profile | Effective memory | Go soft limit | Native inflight | Max segment | Pipeline cap | GOGC |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| `compact` | < 256 MiB | 48 MiB | 32 MiB | 20 MiB | 1 | 75 |
| `balanced` | 256–511 MiB | 96 MiB | 48 MiB | 32 MiB | 2 | 100 |
| `standard` | 512–1023 MiB | 192 MiB | 64 MiB | 32 MiB | 2 | 100 |
| `large` | ≥ 1 GiB | As configured | As configured | As configured | As configured | Runtime default |

CPU constrains the pipeline separately: below 4 cores the cap is the effective milli-CPU rounded up, and from 4 cores upward CPU stops mattering. The final value is the minimum of all three. Detection covers cgroup v1/v2, nested cgroups, inherited parent limits, and fractional CPU quotas. Lite pins 24 MiB Go soft limit, 24 MiB inflight, 20 MiB max segment, and a 1/1 pipeline under both `auto` and `constrained`, keeping its low-memory profile identical across hosts.

The startup log prints the probed values, the selected profile, and every effective budget, so you can confirm a container landed where you expected.

> \[!IMPORTANT\]
> These numbers are soft budgets for the Go heap and media working set, not a guarantee about total container RSS. SQLite, goroutine stacks, the kernel page cache, and FFmpeg child processes in the Full image all sit outside them. For a single-process memory boundary, use the native engine in Core or Lite.

```bash
# Reproduce the low-resource plan
docker run --rm --cpus=1 --memory=192m --memory-swap=192m \
  -v "$PWD/deploy/docker/resource-smoke.toml:/etc/kiln/kiln.toml:ro" \
  kiln:core
```

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🌐 Environment Variables

| Variable | Description |
| --- | --- |
| `KILN_LISTEN` | Overrides the listen address |
| `KILN_PUBLIC_BASE_URL` | Overrides the externally reachable base URL |
| `KILN_DATA_DIR` | Overrides the data directory |
| `KILN_TOKEN_PRIVATE_KEY` / `_FILE` | Injects the Ed25519 private key; `_FILE` takes a path |
| `KILN_TOKEN_PUBLIC_KEY` / `_FILE` | Injects the Ed25519 public key |
| `KILN_RESOURCE_MODE` | Overrides `auto` / `constrained` / `performance` |
| `KILN_RESOURCE_MEMORY_MB` / `KILN_RESOURCE_CPUS` | Overrides detected container resources, for hosts where detection is wrong or to reproduce a tier |
| `KILN_RUNTIME_VARIANT` | Marks the runtime variant; the `core` and `full` images already set it, so it rarely needs setting by hand |
| `KILN_LOG_LEVEL` / `_FORMAT` / `_COLOR` | Log level, `text` or `json`, and coloring (respects `NO_COLOR`) |
| `KILN_DEFAULT_PACKAGER_ENGINE` | Applies only when `packager.engine` is absent from the config |
| `KILN_PLAY_OPEN=1` | Disables playback authentication; debugging only |
| `GOMEMLIMIT` | Always takes precedence over both the config and the automatic plan |

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🔌 API

Four kinds of credentials, each covering its own surface: public endpoints need none, session JWTs serve the admin UI, admin API tokens serve scripts, and path-based playback keys serve player distribution.

| Method | Path | Auth |
| --- | --- | --- |
| GET | `/healthz`, `/readyz` | None |
| GET | `/metrics` | None (`observe.enabled=true`) |
| GET | `/v1/epg.xml`, `/v1/epg.xml.gz`, `/v1/logo/{id}` | None |
| POST | `/v1/auth/login` | None (rate limited) |
| GET | `/v1/me`, `/v1/channels`, `/v1/status` | Session or API token (`read`) |
| GET | `/v1/playlist.m3u` | Session only |
| GET | `/v1/play/{id}/index.m3u8`, `live/{file}`, `u/{upstream}` | Required by default (`?token=` or Bearer) |
| GET | `/p/{token}/playlist.m3u`, `/p/{token}/play/{id}/*` | The playback key in the path |
| GET/POST/PUT/DELETE | `channels/*`, `epg/*`, `egress/*`, `settings`, `access-tokens/*`, `access-logs` under `/v1/admin` | Session or API token, split across `read`, `write`, `delete` |
| POST | `/v1/admin/import/m3u`, `/v1/admin/exports/m3u` | Session or API token (`write`) |
| POST | `/v1/admin/channels/{id}/warmup` (plus `probe`, `preview`), `/v1/admin/epg/refresh`, `/v1/admin/egress/test` | Session or API token (`refresh`) |
| GET/POST/PUT/DELETE | `/v1/admin/api-tokens/*`, `/v1/admin/api-token-logs` | Login session only |
| PUT | `/v1/me/credentials` | Login session only |
| GET | `/admin` | Admin UI |

The table groups the main route families by credential instead of listing every endpoint the admin console uses. An admin API token can only reach registered routes; anything else returns 403. Changing login credentials, managing other tokens, and reading the token audit log accept a login session only, so a token cannot escalate itself. Playback keys are created under Playback Access Control in the admin console, can be scoped to a subset of channels, can be revoked at any time, and every use is recorded in the playback access log.

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🧪 Development

```bash
make ci               # fmt, vet, lint, unit tests — run this before committing
make build-release    # release build with debug info stripped
make docker-images    # build all three variants
make test-complete    # full local verification: extended tests, resource edges, image checks, multi-arch builds
```

`make ci` and GitHub Actions skip performance benchmarks, scheduled fuzzing, fractional CPU, complex cgroup topologies, ARM v6/v7 builds, and external live streams. Those belong to `make test-complete`.

The console's Pinyin and Jyutping reading tables are generated from the Unicode Character Database. Regenerate them only when the tables need to move:

```bash
go run scripts/gen-romanize-data.go                       # fetches Unihan
go run scripts/gen-romanize-data.go -unihan Unihan.zip    # uses a local copy
```

Soak and performance acceptance:

```bash
KILN_TOKEN="$TOKEN" make soak SOAK_ARGS='-output soak.jsonl'   # 24 hours by default
make performance-live                                          # end-to-end at real bitrates
make benchmark-performance                                     # reproducible algorithmic regression baseline
```

The soak harness continuously fetches each channel's master playlist, media playlist, and newest segment, checks whether the media sequence advances or regresses, and writes HTTP errors, stalls, discontinuities, and samples to JSONL. It exits non-zero when any channel keeps failing. `KILN_SOAK_USERNAME` and `KILN_SOAK_PASSWORD` avoid putting credentials on the command line.

Live-stream testing reads its sources only from the gitignored `configs/local.toml`. Results include CDN, proxy, and machine variance, which makes them suitable for end-to-end acceptance but not as a hard CI performance gate.

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 📁 Project Structure

```text
apps/docs/         Documentation site
apps/server/       Service entry point
apps/soak/         Long-running soak harness
modules/           Domain modules (packager, pull, auth, epg, egress, …)
configs/examples/  Sample configuration and key files
deploy/docker/     Dockerfile, Compose template, verification scripts
scripts/           Password hashing, key generation, table generation, console tests
```

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🤝 Contributing

Changes start as an issue, and whether a pull request follows is decided there. It keeps you from writing two hundred lines in the wrong direction. The development environment, the `make ci` bar and the commit message format are in the [contributing guide][contributing-link].

Never open a public issue for a security problem. Use the private channel described in the [security policy][security-link].

Taking part means accepting the [code of conduct][coc-link].

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 📝 License

Copyright © 2026-present [Babywbx][profile-link].<br/>
This project is licensed under [AGPL-3.0-only](./LICENSE).

<!-- LINK GROUP -->

[back-to-top]: https://img.shields.io/badge/-BACK_TO_TOP-151515?style=flat-square
[coc-link]: ./.github/CODE_OF_CONDUCT.en.md
[contributing-link]: ./.github/CONTRIBUTING.en.md
[docs-link]: https://kiln.wbxdocs.com
[github-contributors-link]: https://github.com/babywbx/Kiln/graphs/contributors
[github-contributors-shield]: https://img.shields.io/github/contributors/babywbx/Kiln?color=c4f042&labelColor=black&style=flat-square
[github-forks-link]: https://github.com/babywbx/Kiln/network/members
[github-forks-shield]: https://img.shields.io/github/forks/babywbx/Kiln?color=8ae8ff&labelColor=black&style=flat-square
[github-issues-link]: https://github.com/babywbx/Kiln/issues
[github-issues-shield]: https://img.shields.io/github/issues/babywbx/Kiln?color=ff80eb&labelColor=black&style=flat-square
[github-lastcommit-link]: https://github.com/babywbx/Kiln/commits/main
[github-lastcommit-shield]: https://img.shields.io/github/last-commit/babywbx/Kiln?labelColor=black&style=flat-square
[github-license-link]: https://github.com/babywbx/Kiln/blob/main/LICENSE
[github-license-shield]: https://img.shields.io/github/license/babywbx/Kiln?color=white&labelColor=black&style=flat-square
[github-release-link]: https://github.com/babywbx/Kiln/releases
[github-stars-link]: https://github.com/babywbx/Kiln/network/stargazers
[github-stars-shield]: https://img.shields.io/github/stars/babywbx/Kiln?color=ffcb47&labelColor=black&style=flat-square
[go-version-link]: https://github.com/babywbx/Kiln/blob/main/go.mod
[go-version-shield]: https://img.shields.io/github/go-mod/go-version/babywbx/Kiln?color=369eff&labelColor=black&style=flat-square
[profile-link]: https://github.com/babywbx
[security-link]: ./.github/SECURITY.en.md
[variant-doc]: https://kiln.wbxdocs.com/en/guide/variants/
