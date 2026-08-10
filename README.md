<div align="center"><a name="readme-top"></a>

<img src="./apps/docs/public/icon.webp" alt="Kiln" width="152" height="152" />

# Kiln

轻量的流媒体汇聚网关，把自建上游的 HLS 与 DASH 频道<br/>
统一成一份带鉴权的 M3U 播放列表和随取随用的 HLS 输出。

[English](./README.en.md) · [在线文档][docs-link] · [报告问题][github-issues-link] · [更新日志][github-release-link]

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
<summary><kbd>目录</kbd></summary>

#### TOC

- [📋 概述](#-概述)
- [✨ 特性](#-特性)
- [🚀 快速开始](#-快速开始)
- [📦 安装脚本](#-安装脚本)
- [🐳 Docker](#-docker)
- [🪟 Windows 服务](#-windows-服务)
- [⚙️ 配置](#️-配置)
- [🌐 环境变量](#-环境变量)
- [🔌 API](#-api)
- [🧪 开发](#-开发)
- [📁 项目结构](#-项目结构)
- [📝 许可证](#-许可证)

####

<br/>

</details>

## 📋 概述

这个项目解决一个问题：自建上游的频道格式不统一、没有鉴权、还得一直挂着拉流。Kiln 站在上游和播放器中间，把它们收成一个入口。

```text
自建上游 (HLS / DASH)  →  Kiln  →  播放器 / IPTV 客户端
                            │
                            ├─ 鉴权：口令、会话 JWT、API Token、播放密钥
                            ├─ 拉流：按需启动，无人观看时回收
                            ├─ 媒体：本地解密，原生重封装为 HLS
                            └─ 分发：M3U 播放列表、EPG、管理控制台
```

DASH 的解密与重封装由 Go 原生实现，默认不需要 FFmpeg。整套服务是单个二进制，最小的镜像只有 3.8 MB，在 64 MiB 容器里也能正常播放。

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## ✨ 特性

| 特性 | 说明 |
| --- | --- |
| 原生媒体管线 | HLS 同源分片代理；DASH 按 `kid:key` 本地解密后重封装为 HLS，全程不依赖 FFmpeg |
| 低延迟与多轨 | LL-HLS（CMAF part、delta playlist、blocking reload）、ABR、多音轨、TTML→WebVTT 字幕 |
| 按需拉流 | `on_demand` 在无人观看时回收上游连接，`autostart` 为常看频道预热 |
| 断流自愈 | 媒体停滞探测、指数退避重启、发布代际隔离，播放器自动换代不需要手动刷新 |
| 完整鉴权 | bcrypt 口令、Ed25519 会话 JWT、只展示一次的管理员 API Token、路径式播放密钥 |
| 分发与审计 | 可限定频道范围的播放密钥，M3U 批量导入导出，播放访问日志与 API Token 操作审计 |
| 播放列表与 EPG | 生成按范围过滤的 M3U 并自动关联 XMLTV，内置多组台标候选源 |
| 出站代理 | 按域名或频道路由 HTTP / SOCKS，频道编辑页可直接新建线路并测试连通性 |
| 管理控制台 | 响应式 Web UI，频道预热与预览，拼音、粤拼与简繁互通搜索，静态资源压缩传输 |
| 资源自适应 | 按容器实际内存与 CPU 自动收紧内存预算和并发，只下压、不上调 |
| 跨平台部署 | Linux、macOS、Windows 单二进制，Windows 自带服务安装与失败重启 |
| 可观测 | `/v1/status`、Prometheus `/metrics`、可选 OTLP traces、`/healthz` 与 `/readyz` |

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🚀 快速开始

下面是最短路径，逐项配置、部署方案和 API 细节见[在线文档][docs-link]。

需要 Go 1.26+。原生 DASH 不需要 ffmpeg，只有兼容回退才需要。

```bash
git clone https://github.com/babywbx/Kiln.git
cd Kiln
go run ./apps/server -config configs/examples/kiln.toml
```

服务默认监听 `0.0.0.0:8080`，示例账号 `admin` / `admin`，管理界面在 `/admin`。

登录拿到 token 后就能取播放列表和播放地址：

```bash
TOKEN=$(curl -s http://127.0.0.1:8080/v1/auth/login \
  -H 'content-type: application/json' \
  -d '{"username":"admin","password":"admin"}' | jq -r .token)

curl -s http://127.0.0.1:8080/v1/channels -H "authorization: Bearer $TOKEN" | jq
curl -s http://127.0.0.1:8080/v1/playlist.m3u -H "authorization: Bearer $TOKEN"
curl -s "http://127.0.0.1:8080/v1/play/hls-demo/index.m3u8?token=$TOKEN"
```

生成生产用的口令哈希和 JWT 密钥：

```bash
go run scripts/hash-password.go 'your-password'
go run scripts/gen-jwt-keys.go ./secrets   # 写出 ed25519.pem / ed25519.pub.pem
```

> \[!TIP\]
> 不配置密钥也能跑：进程会在 `{data_dir}/auth/` 下自动生成 Ed25519 密钥对，私钥权限 `0600`。

脚本或命令行长期调用时，在「设置 → 管理员 API Token」创建独立 Token。明文只展示一次，服务端仅保存 SHA-256 摘要，可分别授予 `read`、`write`、`delete`、`refresh` 权限，并设置有效期、轮换或撤销。API Token 不能修改登录凭据，也不能管理其他 Token。

```bash
curl -s http://127.0.0.1:8080/v1/admin/channels \
  -H "authorization: Bearer kiln_v1_..." | jq
```

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 📦 安装脚本

Linux 与 macOS 一条命令装好，重复运行即升级：

```bash
curl -fsSL https://raw.githubusercontent.com/babywbx/Kiln/main/install.sh | sh -s -- --lang zh
```

拉取超时或失败时，换镜像地址：

```bash
curl -fsSL https://ghfast.top/https://raw.githubusercontent.com/babywbx/Kiln/main/install.sh | sh -s -- --lang zh
```

脚本只做四件事：探测平台、选择可用的下载源、校验 `SHA256SUMS`、原子替换二进制。默认不需要 sudo，装到哪里、要做什么都会先展示、确认后才动手。

<details>
<summary><kbd>更多选项</kbd></summary>

<br/>

| 参数 | 说明 |
| --- | --- |
| `--yes` | 静默安装 |
| `--version <v>` | 固定版本 |
| `--lite` | lite 变体（仅 Linux） |
| `--dir <path>` | 自定义安装目录 |
| `--mirror <base>` | 手动指定下载镜像 |
| `--service` | 注册 systemd 服务并开机自启（需 root） |
| `--uninstall` | 卸载 |
| `--dry-run` | 试运行：展示并模拟每一步，不写入任何文件 |

设为开机自启的系统服务：

```bash
curl -fsSL https://ghfast.top/https://raw.githubusercontent.com/babywbx/Kiln/main/install.sh -o /tmp/kiln-install.sh
sudo sh /tmp/kiln-install.sh --yes --service --lang zh
```

</details>

Windows 从 [Releases][github-release-link] 下载 zip 包，参阅 [Windows 服务](#-windows-服务)。

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🐳 Docker

三个镜像边界明确，共用同一套配置模型和原生媒体模块：

| 镜像 | 能力 | 默认 packager | FFmpeg | 定位 |
| --- | --- | --- | --- | --- |
| `kiln:lite` | 配置、登录、M3U、播放 | `native` | 不包含 | scratch 极简运行时，3.8 MB，无数据库 |
| `kiln:core` | 完整 | `native` | 不包含 | 完整管理与观测，纯原生 |
| `kiln:full` | 完整 | `auto` | 内置 9.0 | 优先原生，必要时回退 |
| `kiln:latest` | 完整 | `auto` | 内置 9.0 | `full` 的别名 |

```bash
docker build --target full -f deploy/docker/Dockerfile -t kiln:full .

docker run --rm -p 8080:8080 \
  -v "$PWD/deploy/docker/kiln.docker.toml.example:/etc/kiln/kiln.toml:ro" \
  -v "$PWD/configs/examples/kiln.keys:/etc/kiln/kiln.keys:ro" \
  -v kiln-data:/var/lib/kiln/data \
  kiln:full
```

Lite 面向固定配置的低资源播放节点，可以完全只读运行：

```bash
docker run --rm -p 8080:8080 --read-only \
  --cap-drop=ALL --security-opt=no-new-privileges \
  -v "$PWD/deploy/docker/lite.docker.toml.example:/etc/kiln/kiln.toml:ro" \
  -v "$PWD/configs/examples/kiln.keys:/etc/kiln/kiln.keys:ro" \
  -v kiln-lite-data:/var/lib/kiln \
  kiln:lite
```

它不创建 SQLite，`data_dir` 只存自动生成的登录密钥和临时媒体文件；公开接口只有 `/healthz`、`/readyz`、`/v1/auth/login`、`/v1/playlist.m3u` 和 `/v1/play/*`。配置里出现 `auto`/`ffmpeg` packager、EPG、OTLP 或 pprof 时，进程会在启动时直接拒绝而不是静默忽略。

> \[!NOTE\]
> 镜像默认值只在配置没有填写 `[packager].engine` 时生效。显式写 `auto`、`native` 或 `ffmpeg` 始终优先，同一份配置不会被镜像标签悄悄改变行为。

`core` 多架构覆盖 `linux/amd64`、`linux/arm64`、`linux/arm/v7` 和 `linux/arm/v6`。完整的能力边界、体积和受限容器实测数据见 [Lite、Core 与 Full 性能比较][variant-doc]，`deploy/docker/compose.example.yaml` 提供了 Compose 模板。

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🪟 Windows 服务

Windows 上用内置的服务命令挂到后台，不需要额外的守护工具。安装和卸载需要管理员权限的终端。

```powershell
kiln.exe service install -config C:\kiln\kiln.toml
kiln.exe service start
kiln.exe service status
kiln.exe service stop
kiln.exe service uninstall
```

`-name` 指定服务名（默认 `Kiln`），同一台机器跑多个实例时用它区分，`-display` 改服务显示名。安装后服务为自动启动，并带三级失败重启策略（5 秒、15 秒、60 秒）。

SCM 会在 `system32` 下启动进程，Kiln 会把工作目录切到配置文件所在目录，所以配置里的相对路径（例如 `data_dir = "./data"`）仍然相对配置文件解析。安装时记录的是配置文件的绝对路径，之后移动二进制或配置需要重新安装。

服务模式下标准输出会被 SCM 丢弃，日志改写到配置目录下的 `kiln.log`，超过 16 MB 时在下次启动时轮转为 `kiln.log.1`。

> \[!NOTE\]
> Windows 发行版不含 ffmpeg。`[packager].engine` 为 `auto` 时会自动走原生引擎；确实需要兼容回退时，自行安装 ffmpeg 并加入 `PATH`。

对外提供服务要放行入站端口，Windows 防火墙默认拦截：

```powershell
New-NetFirewallRule -DisplayName "Kiln" -Direction Inbound -LocalPort 8080 -Protocol TCP -Action Allow
```

卸载只移除服务注册，`data_dir`、日志和配置需要自行清理。

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## ⚙️ 配置

示例文件在 `configs/examples/kiln.toml` 和 `kiln.jsonc`，两种格式等价。本地私有配置可以放 `configs/local.toml`，已在 gitignore 中。

| 配置项 | 说明 |
| --- | --- |
| `upstreams[].base_url` | 指向上游服务，频道用 `upstream` + `path` 引用 |
| `[packager].engine` | `native` 完全不装 ffmpeg；`auto` 优先原生并在必要时回退 |
| `[packager].keys_file` | 全局 `kid:key` 文件，每行一对；相对路径按 `kiln.toml` 所在目录解析 |
| `[packager].ll_hls` | 开启 CMAF part、delta playlist 与 blocking reload，`part_target_ms` 定 part 时长 |
| `[packager].inflight_bytes` | 跨频道的分片内存预算，决定峰值内存与 4K 冷启动速度的取舍 |
| `[ffmpeg].mode` | `native` 执行本机二进制；`docker` 由 Kiln 启动指定镜像，不需要 wrapper |
| `[auth]` | Ed25519 会话 JWT，可用 `token_private_key_file` 或环境变量注入 |
| `[epg]` | 磁盘 / 内存 / 关闭缓存三选一；内置源默认全部停用，可逐源选择直连或代理 |
| `[egress]` | 出站默认策略与按域名的路由规则，`[[proxies]]` 定义可复用线路 |
| `[observe]` | `otlp_endpoint` 开启 OTLP/HTTP trace 导出，不写入原始 URL、token 或查询串 |
| `[debug.pprof]` | 仅诊断时启用，监听地址必须是 loopback，使用独立端口与 mux |

媒体密钥文件在启动时一次性完整校验，修改后需要重启，key 不会出现在管理 API。频道级 `keys` 字段已移除。

### 资源自适应

`server.resource_mode` 只有三个取值：推荐的 `auto`、强制低资源的 `constrained`，以及完全退出自适应的 `performance`。`auto` 按有效内存选择内部档位，再独立应用 CPU 上限，**只会下压，不会把你配置的较低数值调高**。

| 启动日志档位 | 有效内存 | Go 软目标 | Native inflight | 单段上限 | 流水线上限 | GOGC |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| `compact` | < 256 MiB | 48 MiB | 32 MiB | 20 MiB | 1 | 75 |
| `balanced` | 256–511 MiB | 96 MiB | 48 MiB | 32 MiB | 2 | 100 |
| `standard` | 512–1023 MiB | 192 MiB | 64 MiB | 32 MiB | 2 | 100 |
| `large` | ≥ 1 GiB | 保持配置 | 保持配置 | 保持配置 | 保持配置 | 运行时默认 |

CPU 再独立限制流水线：低于 4 核时取有效 milli-CPU 向上取整，4 核起不再下压，最终值取三者最小。探测覆盖 cgroup v1/v2、嵌套 cgroup、父级继承限制和小数 CPU quota。Lite 无论 `auto` 还是 `constrained` 都固定使用 24 MiB Go 软目标、24 MiB inflight、20 MiB 单段和 1/1 流水线，以保证跨宿主机的一致低内存特征。

启动日志会打印实际探测值、生效档位和全部预算，方便核对容器是否进入预期档位。

> \[!IMPORTANT\]
> 这些数字是 Go 堆和媒体工作集的软预算，不是容器总 RSS 保证。SQLite、goroutine 栈、内核页缓存和 Full 镜像启动的 FFmpeg 子进程都在预算之外。需要单一进程内存边界时，用 Core 或 Lite 的原生引擎。

```bash
# 复现低资源策略
docker run --rm --cpus=1 --memory=192m --memory-swap=192m \
  -v "$PWD/deploy/docker/resource-smoke.toml:/etc/kiln/kiln.toml:ro" \
  kiln:core
```

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🌐 环境变量

| 变量 | 说明 |
| --- | --- |
| `KILN_LISTEN` | 覆盖监听地址 |
| `KILN_PUBLIC_BASE_URL` | 覆盖对外可访问的基础 URL |
| `KILN_DATA_DIR` | 覆盖数据目录 |
| `KILN_TOKEN_PRIVATE_KEY` / `_FILE` | 注入 Ed25519 私钥，`_FILE` 传路径 |
| `KILN_TOKEN_PUBLIC_KEY` / `_FILE` | 注入 Ed25519 公钥 |
| `KILN_RESOURCE_MODE` | 覆盖 `auto` / `constrained` / `performance` |
| `KILN_RESOURCE_MEMORY_MB` / `KILN_RESOURCE_CPUS` | 覆盖容器资源探测结果，用于宿主机探测不准或复现某一档位 |
| `KILN_RUNTIME_VARIANT` | 标记运行时变体，`core` 与 `full` 镜像已内置，通常不需要手动设置 |
| `KILN_LOG_LEVEL` / `_FORMAT` / `_COLOR` | 日志级别、`text` 或 `json`、着色策略（遵守 `NO_COLOR`） |
| `KILN_DEFAULT_PACKAGER_ENGINE` | 仅在配置未填 `packager.engine` 时生效 |
| `KILN_PLAY_OPEN=1` | 关闭播放鉴权，仅供调试 |
| `GOMEMLIMIT` | 始终优先于配置和自动计划 |

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🔌 API

四种凭据各管一段：公开接口不需要凭据，会话 JWT 服务管理界面，管理员 API Token 服务脚本，路径式播放密钥服务播放器分发。

| 方法 | 路径 | 鉴权 |
| --- | --- | --- |
| GET | `/healthz`、`/readyz` | 无 |
| GET | `/metrics` | 无（`observe.enabled=true`） |
| GET | `/v1/epg.xml`、`/v1/epg.xml.gz`、`/v1/logo/{id}` | 无 |
| POST | `/v1/auth/login` | 无（限流） |
| GET | `/v1/me`、`/v1/channels`、`/v1/status` | 会话或 API Token（`read`） |
| GET | `/v1/playlist.m3u` | 仅会话 |
| GET | `/v1/play/{id}/index.m3u8`、`live/{file}`、`u/{upstream}` | 默认需要（`?token=` 或 Bearer） |
| GET | `/p/{token}/playlist.m3u`、`/p/{token}/play/{id}/*` | 路径内的播放密钥 |
| GET/POST/PUT/DELETE | `/v1/admin` 下的 `channels/*`、`epg/*`、`egress/*`、`settings`、`access-tokens/*`、`access-logs` | 会话或 API Token，按 `read`、`write`、`delete` 划分 |
| POST | `/v1/admin/import/m3u`、`/v1/admin/exports/m3u` | 会话或 API Token（`write`） |
| POST | `/v1/admin/channels/{id}/warmup`（及 `probe`、`preview`）、`/v1/admin/epg/refresh`、`/v1/admin/egress/test` | 会话或 API Token（`refresh`） |
| GET/POST/PUT/DELETE | `/v1/admin/api-tokens/*`、`/v1/admin/api-token-logs` | 仅登录会话 |
| PUT | `/v1/me/credentials` | 仅登录会话 |
| GET | `/admin` | 管理界面 |

上表按凭据类型归纳主要路由族，不逐条列出管理控制台使用的全部接口。管理员 API Token 只能访问已登记的路由，未登记的一律 403；修改登录凭据、管理其他 Token 和查看 Token 审计日志只认登录会话，Token 无法自我提权。播放密钥在管理控制台的「播放访问控制」中创建，可以限定频道范围、随时撤销，每次取用都会记入播放访问日志。

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 🧪 开发

```bash
make ci               # fmt、vet、lint、单元测试，提交前跑这个
make build-release    # 去除调试信息的发布构建
make docker-images    # 同时构建三个变体
make test-complete    # 完整本地验证：扩展测试、资源边界、镜像验证、多架构构建
```

`make ci` 和 GitHub Actions 不跑性能 benchmark、定时 fuzz、小数 CPU、复杂 cgroup 拓扑、ARM v6/v7 构建或外部真实流，这些留给 `make test-complete`。

管理控制台的拼音与粤拼索引表是生成产物，数据来自 Unicode 字符数据库，只在需要更新字表时重跑：

```bash
go run scripts/gen-romanize-data.go              # 联网拉取 Unihan
go run scripts/gen-romanize-data.go -unihan Unihan.zip   # 用本地副本
```

长稳与性能验收：

```bash
KILN_TOKEN="$TOKEN" make soak SOAK_ARGS='-output soak.jsonl'   # 默认 24 小时
make performance-live                                          # 真实码率端到端测量
make benchmark-performance                                     # 可重复的算法回归基准
```

soak harness 持续拉取每个频道的主播放列表、媒体播放列表与最新分片，检查媒体序列是否前进或回退，把 HTTP 错误、停滞、discontinuity 和采样写成 JSONL，任一频道持续失败时以非零状态退出。也可以用 `KILN_SOAK_USERNAME` 与 `KILN_SOAK_PASSWORD` 登录，避免把口令写进命令行。

真实流测试只从 gitignored 的 `configs/local.toml` 读取源，结果包含 CDN、代理和机器波动，适合端到端验收，不作为 CI 的硬性性能门。

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 📁 项目结构

```text
apps/docs/         文档站点
apps/server/       服务入口
apps/soak/         长稳测试 harness
modules/           领域模块（packager、pull、auth、epg、egress……）
configs/examples/  示例配置与密钥文件
deploy/docker/     Dockerfile、Compose 模板与验证脚本
scripts/           口令哈希、密钥生成、索引表生成与控制台测试
```

<div align="right">

[![][back-to-top]](#readme-top)

</div>

## 📝 许可证

Copyright © 2026-present [Babywbx][profile-link].<br/>
本项目基于 [AGPL-3.0-only](./LICENSE) 许可证发布。

<!-- LINK GROUP -->

[back-to-top]: https://img.shields.io/badge/-BACK_TO_TOP-151515?style=flat-square
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
[variant-doc]: https://kiln.wbxdocs.com/guide/variants/
