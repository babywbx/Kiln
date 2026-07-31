# Kiln Lite、Core 与 Full 的能力、体积和受限容器性能比较

> 状态：受控本地基准；测试日期：2026-07-23；被测修订：
> `d1b4790b55e232e52c389d5f0e3793bd5d862f96`；架构：`linux/arm64`；
> Go：1.26.5。

## 摘要

本文比较 Kiln Lite、Core 与 Full 三种发行变体的功能边界、构建体积和受限
容器中的内存表现。实验在相同宿主机、容器限制、配置和媒体 fixture 下执行；
每个变体重复 5 轮，每轮完成 20 次加密 DASH→HLS 与 HLS 代理链路，共
100 次链路验证。

结果表明，Lite 不只是缩小了镜像：它通过构建标签移除数据库、管理台、EPG
和遥测等控制面代码，并采用更低的 Go 与媒体工作集预算。在本实验中，Lite
负载后进程 RSS 快照的中位数为 13.01 MiB，较 Core 和 Full 分别低 48.3%
和 48.4%。Core 与未启动 FFmpeg 子进程的 Full 分别为 25.17 MiB 和
25.21 MiB，在当前测量分辨率下没有可解释的运行时差异。

Lite 的 Docker 本地镜像大小为 3.84 MB，较 Core 小 71.1%，较 Full 小
94.2%。Full 的主要静态成本来自内置 FFmpeg；只要原生引擎能够处理输入，
FFmpeg 不会仅因存在于镜像中而持续占用进程内存。但本实验没有启动 FFmpeg
回退进程，因此不能据此推断 Full 在回退路径上的峰值内存或 CPU。

## 1. 研究问题

本次比较回答四个问题：

1. 三个变体分别保留哪些产品能力？
2. Lite 的优化是否只影响二进制和镜像体积？
3. 在相同原生媒体工作负载下，三个变体的进程 RSS 是否存在稳定差异？
4. 当前证据足以支持哪些部署选择，又不能支持哪些性能结论？

本文不把小型 fixture 当作真实码率测试，也不尝试用一次实验代表所有主机、
网络、频道或并发场景。

## 2. 发行变体

三个镜像复用同一套配置模型与原生 HLS/DASH 媒体模块。Lite 只缩减控制面和
非必要依赖，不维护第二套媒体实现。

| 项目 | Lite | Core | Full |
|---|---|---|---|
| 默认 packager | `native` | `native` | `auto` |
| 原生 HLS/DASH | 是 | 是 | 是 |
| FFmpeg | 不包含 | 不包含 | 内置 8.1.2 |
| 登录、M3U、播放 | 是 | 是 | 是 |
| SQLite 数据库 | 否 | 是 | 是 |
| 管理台与管理 API | 否 | 是 | 是 |
| EPG | 否 | 是 | 是 |
| OTLP 与 pprof | 否 | 是 | 是 |
| 基础镜像 | `scratch` | Alpine | Alpine + FFmpeg |
| 主要定位 | 固定配置、低资源播放节点 | 完整纯原生部署 | 完整兼容部署 |

发布策略预期让 `kiln:latest` 指向同版本 `kiln:full`，因此它不构成第四种
运行时实现；当前仓库没有 registry 发布工作流来验证远端标签是否已经满足
这一策略。

Lite 公开 `/healthz`、`/readyz`、登录、M3U 和播放接口。它会拒绝
`auto`/`ffmpeg` packager、EPG、OTLP 和 pprof 配置。Core 与 Full 使用相同
标准构建入口与代码；两者的主要区别是默认 DASH packager 策略以及 Full
镜像内存在 FFmpeg 可执行文件。当前两个二进制因分别嵌入构建时间而不是逐
字节相同，但大小一致。

Core 的“无 FFmpeg”是官方镜像的依赖边界，不是编译期移除兼容代码。自行向
Core 容器提供 FFmpeg 会改变这一边界。`native`/`auto`/`ffmpeg` 选择主要
控制 DASH 打包；HLS 入站使用播放列表改写和上游代理，不应被描述成由同一个
packager “后端”处理。

Full 的 `auto` 会先尝试原生路径，但不是所有原生失败都可以安全回退。例如
多 KID 输入可能明确禁止交给单密钥 FFmpeg 路径，显式字幕选择也可能无法由
兼容引擎履行。

## 3. 方法

### 3.1 构建与来源

所有镜像均从同一 Git 修订重新构建，并通过镜像契约检查：

```text
Lite  sha256:d2953c76044dba2d4451540a64b81a6f58ed4083879f8f1634b164660cc69d5a
Core  sha256:c5211bc3c3551100b2c1c8b788478f7b52e2e1b85004a812743919de39732ec5
Full  sha256:bd3cbdb9b67dfa8d40fb6db3f9e5523cb9fbcb05f79acc8037024246f21bd94c
```

构建和检查命令为：

```bash
make docker-images docker-verify-images
```

当前应用二进制的 SHA-256 为：

```text
Lite  e71194ed58470b54124d809b03214de40b56e361cd6639ab478636d22e1236a0
Core  de738860dd413119bbd8900a63c1adb1427e377ecbd31a4d231f38cb82db2212
Full  62c55ff029e8c9d879f059820e02aaafb1f55d69dfbe4978e99bab32b989918a
```

Core 与 Full 的哈希不同是因为本次逐个目标构建时嵌入的 `built_at` 不同；
二者使用相同标准构建入口、依赖闭包与编译参数，应用二进制大小均为
22,216,866 bytes。

镜像检查验证变体标签、默认 packager、非 root 用户、FFmpeg 边界、Lite
依赖闭包及体积上限。Full 构建还验证 FFmpeg 的 DASH、HLS、CENC、CBCS、
H.264、HEVC 和 AAC 必要能力。

### 3.2 测试环境

| 项目 | 值 |
|---|---|
| 宿主机 | Apple M1 Max，10 核，64 GiB |
| 宿主系统 | macOS 26.5.2（25F84） |
| 容器运行环境 | OrbStack |
| Docker Server | 29.4.0 |
| 容器架构 | `linux/arm64` |
| Docker VM 可见资源 | 10 CPU，8,394,141,696 bytes |
| 单个 Kiln 容器限制 | 1 CPU，128 MiB，swap 与内存相同 |
| 根文件系统 | 只读 |
| Linux capabilities | 全部移除 |
| `no-new-privileges` | 启用 |

测试期间没有运行其他 Kiln 容器。三种变体按交叉顺序执行，以减弱固定测试
顺序造成的缓存偏差。

### 3.3 工作负载

测试使用仓库中的确定性媒体 fixture，而不是外部 CDN：

- 视频为 320×180、25 fps、4 秒、目标 120 kbit/s 的 H.264 测试图；
- 音频为 4 秒、32 kbit/s、双声道 AAC 测试音；
- 加密 DASH 输入经 Kiln 原生引擎输出 HLS；
- HLS 输入经同源播放列表与分片代理；
- 每次迭代抓取 DASH→HLS 的 master、media、init 和 media segment；
- 每次迭代抓取 HLS 代理的 playlist、init 和 media segment；
- 每轮 20 次迭代，每个变体 5 轮，共 100 次完整链路。

三种变体使用相同配置、密钥文件、源站容器和媒体内容。Full 默认使用
`auto`，但该 fixture 可由原生引擎完整处理。独立确认轮次的日志记录
`engine=native_rewrite`，进程表中也只有 Kiln，没有 FFmpeg 子进程。这一设计
隔离了应用变体本身的开销，但不覆盖 FFmpeg 回退成本。

### 3.4 资源档位

在相同的 1 CPU / 128 MiB 容器限制下，Lite 与 Core/Full 有意采用不同的产品
预算。这些预算是变体设计的一部分，不能在比较时人为拉平。

| 生效值 | Lite | Core / Full |
|---|---:|---:|
| 资源档位 | `lite` | `compact` |
| Go 软内存目标 | 24 MiB | 48 MiB |
| Native inflight 上限 | 24 MiB | 32 MiB |
| 单段上限 | 20 MiB | 20 MiB |
| 启动 / 预取流水线 | 1 / 1 | 1 / 1 |
| GOGC | 50 | 75 |
| 主动回收媒体页缓存 | 是 | 是 |

Go 软内存目标不是容器 RSS 上限。进程栈、映射页、SQLite、内核页缓存和
FFmpeg 子进程均可能位于该目标之外。

### 3.5 指标与统计

本次记录以下指标：

- **本地镜像大小**：`docker image inspect --format '{{.Size}}'`；
- **二进制大小**：容器内可执行文件的精确字节数；
- **负载后进程 RSS**：每轮第 20 次迭代完成后读取 PID 1 的 `VmRSS`；
- **cgroup current/peak**：读取 cgroup v2 的 `memory.current` 与
  `memory.peak`；
- **OOM**：读取 `memory.events` 中的 `oom` 与 `oom_kill`；
- **链路成功率**：所有清单与媒体文件非空且格式检查通过。

每个连续指标报告 5 轮的中位数与最小值—最大值。样本量较小，本文只作描述性
比较，不计算显著性，也不把小于轮间波动的差异解释为产品优势。

Docker 的 `.Size` 是当前引擎报告的本地镜像内容大小，不等同于所有 registry
和导出格式下的下载字节数。本文同时报告未压缩的应用二进制大小，避免把镜像
压缩率误解为程序本身的大小。

## 4. 结果

### 4.1 构建体积

MB 使用十进制字节，MiB 使用二进制字节。

| 变体 | Docker 本地镜像 | Kiln 二进制 | 额外 FFmpeg |
|---|---:|---:|---:|
| Lite | 3.84 MB / 3.66 MiB | 9.31 MB / 8.88 MiB | — |
| Core | 13.27 MB / 12.66 MiB | 22.22 MB / 21.19 MiB | — |
| Full | 66.41 MB / 63.33 MiB | 22.22 MB / 21.19 MiB | 109.57 MB / 104.49 MiB |

相对变化：

- Lite 二进制比 Core/Full 二进制小 58.1%；
- Lite 镜像比 Core 镜像小 71.1%；
- Lite 镜像比 Full 镜像小 94.2%；
- Core 镜像比 Full 镜像小 80.0%。

Full 中 FFmpeg 的未压缩文件大小大于 Docker 报告的整体本地镜像大小，是镜像
层压缩的结果，并非测量矛盾。

### 4.2 内存与链路稳定性

| 变体 | 负载后 RSS 中位数（范围） | cgroup peak（缓存敏感）中位数（范围） | 成功链路 | OOM / OOM kill |
|---|---:|---:|---:|---:|
| Lite | 13.01 MiB（11.48–13.33） | 11.40 MiB（9.51–21.89） | 100 / 100 | 0 / 0 |
| Core | 25.17 MiB（25.11–25.27） | 33.27 MiB（12.52–37.38） | 100 / 100 | 0 / 0 |
| Full | 25.21 MiB（21.03–25.35） | 17.82 MiB（12.80–34.13） | 100 / 100 | 0 / 0 |

以 RSS 中位数计算：

- Lite 比 Core 低 48.3%；
- Lite 比 Full 低 48.4%；
- Full 比 Core 高约 0.04 MiB，差异约 0.14%，不足以解释为真实性能差异。

### 4.3 cgroup 数据为何波动较大

`memory.peak` 同时受匿名内存和文件页缓存记账影响。测试使用相同镜像层、相同
fixture，并在交叉顺序中重复创建 cgroup。共享文件页可能已由前一个 cgroup
触碰和记账；媒体页又会在运行时主动回收。采样脚本还通过 `docker exec` 在
被测 cgroup 内短暂执行读取工具，探针本身会产生轻微观察者效应。因此，
cgroup peak 的轮间变化明显大于 PID 1 RSS。

这也解释了为什么 Full 的 cgroup peak 中位数低于 Core。两者应用构建与原生
执行代码相同，RSS 中位数也几乎一致；不能从该 cgroup 排名推断 Full 比 Core
更节省运行内存。本文把 cgroup 数据用于确认容器边界、页缓存波动和 OOM
安全性，主要的变体内存比较采用更稳定的负载后进程 RSS 快照。该快照同样
不是进程生命周期峰值。

### 4.4 对结果的解释

Lite 的内存下降来自运行时结构和预算共同变化，而非单纯压缩二进制：

1. Lite 构建不包含 SQLite、管理台、EPG、OTLP、gRPC 和 protobuf 依赖；
2. Lite 使用静态配置目录与精简 HTTP 控制面；
3. Lite 保留相同原生媒体模块，因此播放兼容性不会因另写一套媒体代码而分叉；
4. Lite 使用 24 MiB Go 软目标、更积极的 GC 和固定单流水线；
5. Core/Full 初始化数据库、完整 API、管理与观测服务，因此具有更高基线。

Core 与 Full 在原生路径上的内存表现近似相同，说明“把 FFmpeg 放入镜像”主要
增加分发与磁盘成本。只有在 Full 实际启动 FFmpeg 子进程后，额外的进程内存和
CPU 才进入容器总账；该成本不受 Kiln 的 Go 软内存目标约束。

### 4.5 官方资源契约复验

完成统一比较后，又从同一批镜像运行了三个正式验收目标。这些测试的资源限制
和负载不同，只用于验证各自产品契约，不能用于三版本性能排名。

| 目标 | 条件 | 新测试结果 |
|---|---|---|
| Lite 媒体 smoke | 1 CPU / 64 MiB，10 次链路 | 10/10；负载后 RSS 12.80 MiB；cgroup peak 21.50 MiB；无 OOM |
| Core 媒体 smoke | 1 CPU / 128 MiB，20 次链路 | 20/20；负载后 RSS 25.34 MiB；cgroup peak 35.38 MiB；无 OOM |
| Full 资源 smoke | 2 CPU / 384 MiB，启动检查 | `balanced` 档位与 FFmpeg advisory-only 警告均通过 |

对应命令为：

```bash
make docker-smoke-lite \
  test-resource-docker-core-media \
  test-resource-docker-full
```

## 5. 部署选择

### 选择 Lite

适用于以下条件：

- 节点只需登录、M3U 和播放；
- 频道可以完全由原生引擎处理；
- 配置由文件管理，不需要运行时数据库和管理台；
- 资源预算、镜像拉取速度或攻击面优先。

当前证据支持在 64 MiB 容器中运行 Lite 的官方 fixture smoke；128 MiB 条件下
的对比实验进一步显示了稳定余量。但这不是任意码率、任意并发下的 64 MiB
保证。

### 选择 Core

适用于需要完整管理、数据库、EPG 和观测能力，同时已经确认所有输入都兼容
原生引擎的部署。与 Full 相比，Core 显著减少镜像体积，但在相同原生路径上
不应预期明显低于 Full 的 Kiln 进程 RSS。

### 选择 Full

适用于输入兼容性优先、需要 FFmpeg 回退的部署。原生可处理输入时，Full 的
Kiln 进程内存与 Core 接近；发生回退时，应按“Kiln 进程 + FFmpeg 子进程 +
页缓存”设置容器限制，并使用真实频道单独验收。

## 6. 结论边界

本次实验可以支持：

- Lite 的体积显著小于 Core 和 Full；
- Lite 的优化同时降低了当前工作负载下的进程 RSS；
- Core 与未启动 FFmpeg 的 Full 在原生路径上没有可解释的 RSS 差异；
- 三个变体都在 1 CPU / 128 MiB 下完成 100 次短链路验证且没有 OOM。

本次实验不能支持：

- Lite、Core 或 Full 的真实 1080p/4K 吞吐排名；
- 多频道并发容量、CPU 效率、首帧时间或长时间内存稳定性；
- Full 的 FFmpeg 回退内存、CPU 或吞吐；
- amd64、arm/v7、arm/v6 与 arm64 之间的性能比较；
- 外部 CDN、代理、网络抖动和上游故障条件下的结论；
- 以 100 次短链路替代 24 小时 soak。

100 次链路嵌套在 5 个容器轮次中，不是 100 个相互独立的统计样本。结果也
不能证明不存在内存泄漏；这需要对同一个长期运行进程测量内存平台值与增长
斜率。

真实部署验收应继续使用 gitignored 的 `configs/local.toml` 与
`scripts/live-performance.sh` 测量冷启动、首个 manifest、吞吐/实时比、离线
解码倍率、RSS 和 CPU，并对目标频道执行 24 小时 soak。真实流数据应单独记录
测试日期、源站、码率、编码、代理路径、机器和变体，不能与本文 fixture
数字混为同一总体。

## 7. 复现

先从目标修订构建并检查镜像：

```bash
make docker-images docker-verify-images
```

单轮比较使用
[`deploy/docker/native-media-runtime-smoke.sh`](../deploy/docker/native-media-runtime-smoke.sh)。
以下函数固定 1 CPU、128 MiB 和 20 次迭代：

```bash
run_variant() {
  variant=$1
  image=$2

  case "$variant" in
    lite)
      profile=lite
      memory_limit=24
      inflight=24
      gc=50
      ;;
    core|full)
      profile=compact
      memory_limit=48
      inflight=32
      gc=75
      ;;
  esac

  KILN_SMOKE_CHECK_RESOURCES=1 \
  KILN_SMOKE_CPUS=1 \
  KILN_SMOKE_MEMORY=128m \
  KILN_SMOKE_MAX_PEAK_BYTES=134217728 \
  KILN_SMOKE_MAX_RSS_KB=131072 \
  KILN_SMOKE_ITERATIONS=20 \
  KILN_SMOKE_VARIANT="$variant" \
  KILN_SMOKE_HEALTHCHECK_MODE=external \
  KILN_SMOKE_EXPECTED_PROFILE="$profile" \
  KILN_SMOKE_EXPECTED_MEMORY_LIMIT_MB="$memory_limit" \
  KILN_SMOKE_EXPECTED_INFLIGHT_MB="$inflight" \
  KILN_SMOKE_EXPECTED_MAX_SEGMENT_MB=20 \
  KILN_SMOKE_EXPECTED_GC_PERCENT="$gc" \
    sh deploy/docker/native-media-runtime-smoke.sh \
      "$image" \
      busybox:1.38.0@sha256:fd8d9aa63ba2f0982b5304e1ee8d3b90a210bc1ffb5314d980eb6962f1a9715d
}

run_variant lite kiln:lite-local
run_variant core kiln:core-local
run_variant full kiln:local
```

正式复测应至少重复 5 轮，并轮换执行顺序。比较不同提交时，还应保持 Docker
版本、主机负载、容器限制、fixture、迭代数和资源模式不变。

## 附录 A：原始数据

RSS 单位为 KiB，cgroup peak 单位为 bytes。

| 变体 | 轮次 | RSS | cgroup peak | OOM | OOM kill | 链路 |
|---|---:|---:|---:|---:|---:|---:|
| Lite | 1 | 13,304 | 11,956,224 | 0 | 0 | 20 / 20 |
| Lite | 2 | 11,752 | 22,958,080 | 0 | 0 | 20 / 20 |
| Lite | 3 | 13,324 | 9,969,664 | 0 | 0 | 20 / 20 |
| Lite | 4 | 13,652 | 13,250,560 | 0 | 0 | 20 / 20 |
| Lite | 5 | 13,456 | 11,010,048 | 0 | 0 | 20 / 20 |
| Core | 1 | 25,716 | 39,190,528 | 0 | 0 | 20 / 20 |
| Core | 2 | 25,872 | 13,127,680 | 0 | 0 | 20 / 20 |
| Core | 3 | 25,776 | 34,881,536 | 0 | 0 | 20 / 20 |
| Core | 4 | 25,828 | 13,201,408 | 0 | 0 | 20 / 20 |
| Core | 5 | 25,744 | 35,164,160 | 0 | 0 | 20 / 20 |
| Full | 1 | 25,960 | 35,790,848 | 0 | 0 | 20 / 20 |
| Full | 2 | 25,812 | 13,496,320 | 0 | 0 | 20 / 20 |
| Full | 3 | 25,744 | 13,418,496 | 0 | 0 | 20 / 20 |
| Full | 4 | 25,904 | 18,685,952 | 0 | 0 | 20 / 20 |
| Full | 5 | 21,532 | 33,882,112 | 0 | 0 | 20 / 20 |

## 附录 B：本地证据

- 镜像构建定义：
  [`deploy/docker/Dockerfile`](../deploy/docker/Dockerfile)
- 镜像能力与体积检查：
  [`deploy/docker/verify-images.sh`](../deploy/docker/verify-images.sh)
- Lite 依赖与体积检查：
  [`deploy/docker/verify-lite.sh`](../deploy/docker/verify-lite.sh)
- 受限容器与 cgroup 采样：
  [`deploy/docker/native-media-runtime-smoke.sh`](../deploy/docker/native-media-runtime-smoke.sh)
- 媒体请求链路：
  [`deploy/docker/native-media-chain-smoke.sh`](../deploy/docker/native-media-chain-smoke.sh)
- Core 资源契约：
  [`deploy/docker/core-media-smoke.sh`](../deploy/docker/core-media-smoke.sh)
- Full FFmpeg 资源说明：
  [`deploy/docker/full-resource-smoke.sh`](../deploy/docker/full-resource-smoke.sh)
- 真实流验收：
  [`scripts/live-performance.sh`](../scripts/live-performance.sh)
