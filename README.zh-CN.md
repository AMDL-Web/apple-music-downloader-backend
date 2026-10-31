# AMDL

<p align="center">
  <a href="https://github.com/AMDL-Web/apple-music-downloader/actions/workflows/ci.yml"><img src="https://github.com/AMDL-Web/apple-music-downloader/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/AMDL-Web/apple-music-downloader/releases"><img src="https://img.shields.io/github/v/release/AMDL-Web/apple-music-downloader?label=release" alt="最新版本"></a>
  <a href="https://github.com/AMDL-Web/apple-music-downloader/pkgs/container/apple-music-downloader"><img src="https://img.shields.io/badge/ghcr.io-amd64%20%2B%20arm64-blue" alt="GHCR 容器镜像"></a>
  <img src="https://img.shields.io/github/go-mod/go-version/AMDL-Web/apple-music-downloader" alt="Go 版本">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-AGPL--3.0-green" alt="AGPL-3.0"></a>
</p>

<p align="center"><a href="README.md">English</a></p>

amdl 把 Apple Music 上的音乐下载到你自己的磁盘。把单曲、专辑、歌单、艺人或电台链接贴进它的
网页控制台，或在那里直接搜索曲库，它会解析出曲目，下载加密媒体，解密、重封装、打标签，保存成完整的
`.m4a` 文件。

这个仓库容纳整个 AMDL 项目。每个产品在 `apps/` 下有自己的目录，共用 `internal/` 下的
同一套 Go 核心。

## 产品

### 服务端

`amdl serve`（`apps/amdl`）是要部署的后端：一个 Go 进程，带一个 SQLite 数据库、`/api/v1` 上的 REST +
Server-Sent Events 接口，以及同一端口上的网页控制台。它以容器镜像发布，镜像由 `deploy/server/`
构建。[快速开始](#快速开始)讲怎么运行它，本页其余部分都在介绍它。

### 网页控制台

`apps/console` 是浏览器客户端：一个免构建的 Preact 应用，嵌入服务端，在 `/console/` 提供。
它只使用公开 API。设计约定见 [apps/console/DESIGN.md](apps/console/DESIGN.md)。

### 命令行

`apps/amdl` 构建出 `amdl`，一个面向 agent 和脚本的 API 客户端：每个结果都是 stdout 上的
JSON，错误以 JSON 写到 stderr，退出码区分失败类别，`amdl cli search "<任务>"` 能找到对应
的命令。`amdl mcp` 把同样的能力作为 MCP 工具提供；在终端里直接运行 `amdl` 会打开给人用的交互界面，`make tui-web` 把它编译给浏览器，数据来自桌面演示版同一个虚构后端。没有单独部署服务器时，`amdl service install`
会把后端作为登录即启动的后台服务跑在本机，`amdl wrapper install` 再给它配一个自己的 wrapper：
由后端在 QEMU 虚拟机里运行的上游 wrapper-lite（[docs/install.md](docs/install.md#a-wrapper-amdl-runs)）。
详见 [docs/cli.md](docs/cli.md)。

### 官网

`apps/site` 是项目官网，有英文和简体中文两版：由一个零依赖的 Node 脚本构建的静态页面。构建和本地预览方法见
[apps/site/README.md](apps/site/README.md)。

### 桌面应用

`apps/desktop` 是一个面向 macOS、Windows 和 Linux 的原生应用，用 [MyGo](https://github.com/egoist/mygo)
的原生界面绘制。它可以作为远程服务端的客户端运行，也可以作为单机后端，在进程内跑下载流水线、连接
wrapper：可以是这台电脑上的那个（应用的设置引导会像 `amdl wrapper install` 一样连同 QEMU 和 ffmpeg 一起装好），也可以是别的机器上的。单机模式通过 `internal/local` 在本机回环端口上运行后端；如果这台电脑上已有后端在跑（比如
`amdl service`），就直接使用它，所以应用和命令行共用一个后端、一份下载记录；应用自己的包放在
apps/desktop/internal 下，只归它用。每个版本都附带它的 macOS、Windows 和 Linux 安装包，目前未签名
（[docs/install.md](docs/install.md#the-desktop-app)）；`make desktop-web` 把同一个应用编译给浏览器，
作为演示版，数据来自内存里的虚构后端。

## 仓库结构

| 路径 | 内容 |
| --- | --- |
| `apps/` | 一个产品一个目录：`amdl`（服务端和命令行）、`console`、`desktop` 和 `site`（官网）。非 Go 应用在自己的目录里保留自己的构建系统。 |
| `internal/` | 所有产品共用的 Go 核心：HTTP API、作业、下载流水线、Apple 与 wrapper 客户端、存储和配置。[Architecture](docs/architecture.md#2-packages) 列出了每个包。 |
| `deploy/` | Compose 文件，以及 `deploy/server/` 下服务端的 Dockerfile 和入口脚本。 |
| `configs/` | 每个安装据以生成配置的示例配置和钩子模板。 |
| `docs/` | 参考文档和操作手册（[索引](docs/README.md)）。 |
| `tools/` | 仓库工具：文档检查、文件拆分、Temari 一致性检查、README 配图生成。 |

服务端需要准备：

- Apple Music 订阅。
- 一个 wrapper：保存 Apple Music 登录状态的上游服务。它跑在 Linux（x86_64 或 arm64）上，
  原生和 Docker 启动器需要 user namespace（`unshare -Ur true` 必须能成功）；没有它的主机，
  例如群晖 DSM，改用 wrapper-manager v2 的 QEMU 变体。见 [Wrapper](docs/deployment.md#the-wrapper)。
  在 macOS、Windows 或 Linux 上用二进制安装时，也可以让 amdl 自己在 QEMU 里跑一个（`amdl wrapper install`）。
- 带 Compose 的 Docker，或者 Go（版本见 [`go.mod`](go.mod)）加 `ffmpeg`。

## 快速开始

两种运行方式对照：

| | 二进制 | Docker |
| --- | --- | --- |
| 适合 | 台式机或笔记本（macOS、Linux、Windows），不装 Docker | NAS 或服务器，和 wrapper 放在一起 |
| 安装 | `curl -fsSL https://raw.githubusercontent.com/AMDL-Web/apple-music-downloader/main/deploy/install/install.sh \| sh`（PowerShell 见指南） | 复制 [`deploy/docker-compose.yml`](deploy/docker-compose.yml) |
| 运行方式 | 登录即启动的后台服务：`amdl service install --wrapper <url>` | `docker compose up -d` |
| ffmpeg | 自己安装；`amdl doctor` 会给出安装命令 | 镜像自带 |
| wrapper | 用别处的，或让 amdl 在 QEMU 里跑一个：`amdl wrapper install`；[用模拟模式试用](docs/install.md#try-it-without-a-wrapper)时不需要 | 同一台机器上的 compose 服务 |
| 指南 | [docs/install.md](docs/install.md) | 下文和 [docs/deployment.md](docs/deployment.md) |

本节下面讲 Docker 方式。仓库里的 [`deploy/docker-compose.yml`](deploy/docker-compose.yml) 用 GHCR 上的预构建镜像运行 amdl，两种
wrapper 部署方式都以注释掉的服务写在里面。把它复制到一个空目录；第一次启动会在旁边建出
`configs/` 和 `data/`。

### 搭配 wrapper-manager v2（推荐）

[wrapper-manager v2](https://github.com/WorldObservationLog/wrapper-manager/tree/v2) 替你运行
wrapper-lite，并且可以在控制台里登录。

1. 在 `docker-compose.yml` 里取消注释 `wrapper-manager` 服务和文件末尾的 `volumes:` 块，并设置
   `AMDL_WRAPPER_ADDRESS: "http://wrapper-manager:8080"`。
2. 两个一起启动：

   ```bash
   docker compose up -d
   ```

   Compose 会从 `v2` 分支构建 wrapper-manager，它第一次启动时会下载 wrapper-lite 的程序包，
   所以 wrapper 主机需要能上网。
3. 打开 `http://localhost:18080/`，进入 **Settings › Account**，用 Apple ID 登录。Apple 要求
   两步验证码时，控制台会向你询问。
4. 在 **New download** 里粘贴链接。下载完成的文件在 `./data/downloads`。

### 搭配单独的 wrapper-lite

[wrapper-lite](https://github.com/WorldObservationLog/wrapper/tree/lite) 只服务一个账号，
在它自己的命令行里登录，不能从控制台登录。

- **作为 Compose 服务：** 取消注释 `wrapper-lite` 服务，设置
  `AMDL_WRAPPER_ADDRESS: "http://wrapper-lite:12340"`，并在 `docker-compose.yml` 旁边的
  `.env` 文件里写上 `USERNAME` 和 `PASSWORD`，供第一次启动登录用。Apple 要求两步验证码时，
  把验证码写进 `./data/wrapper` 里的 `2fa.txt`。构建镜像要下载 Android NDK，比较慢。
- **直接跑在主机上：** 保持默认的 `AMDL_WRAPPER_ADDRESS`，先登录一次，再启动服务（上游给出的命令）：

  ```bash
  ./wrapper-lite-rootless --login user:pass --code-from-file --base-dir /data
  ./wrapper-lite-rootless --base-dir /data --host 0.0.0.0 --port 12340
  ```

控制台的 **Settings › Account** 也会给出登录命令。之后打开 `http://localhost:18080/` 粘贴链接即可。

### 从源码运行

```bash
go run ./apps/amdl serve
```

它读取 `configs/config.yaml`（第一次启动时由
[`configs/config.example.yaml`](configs/config.example.yaml) 生成），监听 `server.listen`
指定的地址。把 `wrapper.address` 指向你的 wrapper。

镜像标签、挂载目录、`PUID`/`PGID`、反向代理以及 wrapper 各方案的细节见
[docs/deployment.md](docs/deployment.md)（英文）。

## 从 1.x 升级

2.0 改了 API、配置项和 wrapper。amdl 通过 HTTP 对接上游的 wrapper-manager v2 或 wrapper-lite，
不再使用 1.x 的 wrapper-manager 分支；第一次启动会迁移数据库，迁移后 1.x 无法再读取。升级前请
备份数据库，并阅读 [docs/upgrading-to-2.0.md](docs/upgrading-to-2.0.md)（英文）。

## 工作原理

<p align="center">
  <img src="./docs/assets/architecture.svg" width="100%"
       alt="架构：网页控制台调用 amdl 的 REST 与 SSE 接口；amdl 把作业存进 SQLite，让每首曲目走流水线，把文件写进 downloads_dir；wrapper 提供密钥模板、清单、歌词和许可证；Apple 的 amp-api 提供曲库数据，其 CDN 提供加密分片">
</p>

**wrapper。** amdl 自己不持有 Apple 账号，也没有密钥。对每个 FairPlay 密钥，它向 wrapper
要一份白盒模板（`/key`），然后在本进程内用 `internal/fairplay` 自己解密样本；
`internal/fairplay` 是 [Temari](https://github.com/WorldObservationLog/Temari) 的纯 Go 翻译。
wrapper 还提供歌词、AAC-LC 许可证，以及 Hi-Res ALAC 曲目的设备清单。没有 wrapper 时 amdl
也能启动；`GET /api/v1/wrapper/status` 和控制台的 Settings 页面会显示它是否可达、是否已登录。

**Apple 曲库。** 所有曲库读取都发往 `amp-api.music.apple.com`，用的是从 `music.apple.com`
抓取的令牌；amdl 不需要 Apple 开发者密钥。这个接口没有公开文档，可能随时变化。

<p align="center">
  <img src="./docs/assets/track-download.svg" width="100%"
       alt="一首曲目的状态图：先解析，然后每个编码依次经过获取阶段（选清单、占用输出路径、取一次封面和歌词、下载）和解密阶段（解密、重封装、校验、打标签、保存），某阶段次数用完就换下一个编码；结局为 completed、skipped_existing、failed 或 cancelled">
</p>

**一首曲目。** 每首曲目先解析，再下载和解密。解密后的样本直接通过管道送进 `ffmpeg` 重封装，
整首曲目的明文副本从不落盘；校验和打标签都在 `download.temp_dir` 里完成，只有成品文件才移进
`download.downloads_dir`。如果那个文件已经存在，这首曲目会被跳过，不再获取封面和歌词。

**编码回退。** `download.quality_priority` 按顺序列出要尝试的编码。每个编码的下载阶段和解密
阶段各有自己的 `download.max_attempts` 次数，两次尝试之间会退避等待。开启
`download.codec_alternative` 时，一个编码用完次数就交给下一个，并在最后自动追加 AAC-LC 兜底；
关闭时只尝试第一个编码。

**并发。** 下载、解密、wrapper 请求和运行中的作业，各自共用一个进程级的池，所有作业共享。
池满时优先分给最早提交的作业，所以作业往往一个接一个完成。

阶段、重试、并发池和内存模式见 [docs/download-pipeline.md](docs/download-pipeline.md)（英文）。
进程生命周期和包的边界见 [docs/architecture.md](docs/architecture.md)（英文）。

## 文件存放位置

成品文件放在 `download.downloads_dir` 下（Compose 部署时是 `./data/downloads`）。单曲、专辑、
艺人、歌单和电台各有一个路径模板，由 `{ArtistName}`、`{AlbumName}`、`{SongName}` 等变量组成。
独立封面和 `.lrc` / `.ttml` 歌词文件可以存到音频旁边。模板、可用变量和附属文件选项见
[`configs/config.example.yaml`](configs/config.example.yaml) 与
[docs/download-pipeline.md](docs/download-pipeline.md#output-paths)（英文）。

## API

[`internal/api/openapi.yaml`](internal/api/openapi.yaml) 是所有请求和响应格式的权威来源。下表
所有路径都在 `/api/v1` 之下。`/console/` 上的网页控制台是这套 API 唯一的浏览器客户端：响应不带
CORS 头。

| | |
| --- | --- |
| **下载** | `POST /downloads` · `GET /downloads` · `GET/DELETE /downloads/{id}` · `POST /downloads/{id}/cancel` · `POST /downloads/{id}/retry` |
| **事件** | `GET /downloads/{id}/events` 单个作业 · `GET /downloads/events` 总览流（SSE） |
| **链接预览** | `POST /lookup`：不建作业，预览链接的标题、艺人和封面 |
| **搜索** | `GET /search`：在曲库里搜歌曲、专辑、歌单和艺人 · `GET /artists/{id}/albums`：分页列出艺人的专辑 · `GET /albums/{id}`：专辑的歌曲列表 |
| **音质探测** | `POST /quality`：不建作业，探测链接的编码和音质 |
| **wrapper** | `GET /wrapper/status` · `POST /wrapper/login` · `POST /wrapper/login/{login_id}/2fa` · `POST /wrapper/logout` |
| **配置** | `GET /config` · `PUT /config` · `POST /config/reload` · `GET /hooks` |
| **资料库同步** | `GET /library-sync` · `POST /library-sync/reset` |
| **系统** | `GET /health` · `GET /system` · `GET /stats` · `GET /logs` · `GET /logs/stream` |

提交一张专辑并跟踪进度：

```bash
curl -X POST http://localhost:18080/api/v1/downloads \
  -H 'Content-Type: application/json' \
  -d '{"urls":["https://music.apple.com/cn/album/1580904295"]}'

curl -N http://localhost:18080/api/v1/downloads/{job_id}/events
```

错误格式、事件流续传、作业级覆盖参数，以及每个接口的 curl 示例见 [docs/api.md](docs/api.md)（英文）。

## 配置

只有一个文件 `configs/config.yaml`，第一次启动时由
[`configs/config.example.yaml`](configs/config.example.yaml) 生成。示例文件的注释写明了每个配置项
的可选值、单位和默认值。运行时配置项通过 `PUT /api/v1/config`（控制台的 Settings 页面）修改，
这会重写整个文件并丢掉注释；启动配置项要重启才生效。`AMDL_SERVER_LISTEN`、`AMDL_WRAPPER_ADDRESS`、
`AMDL_WRAPPER_DATA_DIR` 和 `AMDL_DATABASE_PATH` 可以覆盖对应的配置项。下载电台需要订阅令牌：后端自己运行
wrapper，或 `wrapper.data_dir` 指向 wrapper 的文件时，直接从 wrapper 读取；否则用
`catalog.media_user_token` 里填的。详见 [docs/configuration.md](docs/configuration.md)（英文）。

## 自动化

- **作业钩子**（[`configs/hooks.yaml`](configs/hooks.yaml)）在作业入队或结束时调用 webhook 或
  运行命令，比如刷新媒体服务器。
- **资料库同步**监视你的 Apple Music 资料库，你每加一首歌，就下载它所在的专辑。需要订阅令牌：
  wrapper 的，或 `catalog.media_user_token`。

两者默认都关闭。见 [docs/automation.md](docs/automation.md)（英文）。

## 限制

- **只支持音频。** 不支持音乐视频：在 L3 下流水线只能拿到低分辨率视频。
- **单用户，无账号，无鉴权。** 所有接口都是开放的，这是有意为之。请在前面放反向代理或网关做访问控制。
- **电台是滚动列表。** 电台解析出的是 Apple 此刻提供的曲目，不是固定曲目表。Apple Music 1
  这类直播电台会明确报错。
- **曲库接口没有公开文档。** Enhanced HLS 清单和动态封面来自 `amp-api.music.apple.com`，
  可能毫无预警地变化。

## 文档

| | |
| --- | --- |
| [docs/deployment.md](docs/deployment.md) | Docker、wrapper、挂载目录、`PUID`/`PGID`、反向代理、发布与镜像标签 |
| [docs/configuration.md](docs/configuration.md) | 配置文件、默认值、环境变量覆盖、运行时与启动配置项、重新加载 |
| [docs/upgrading-to-2.0.md](docs/upgrading-to-2.0.md) | 从 1.x 起的每一处破坏性变更，附升级清单 |
| [docs/install.md](docs/install.md) | 不用 Docker 安装 `amdl`：安装脚本、ffmpeg、amdl 自己运行的 wrapper、后台服务 |
| [docs/cli.md](docs/cli.md) | `amdl` 命令行、JSON 输出与退出码、MCP 服务和终端界面 |
| [docs/api.md](docs/api.md) | 每个接口的 curl 示例、作业覆盖参数、错误码、SSE 语义 |
| [docs/architecture.md](docs/architecture.md) | 进程生命周期、包、API 约定、存储 |
| [docs/download-pipeline.md](docs/download-pipeline.md) | 阶段、重试、编码回退、并发、内存模式、歌词、输出路径 |
| [docs/automation.md](docs/automation.md) | 作业钩子与资料库同步 |
| [docs/benchmarks.md](docs/benchmarks.md) | 解密后处理与端到端的实测结果 |
| [docs/references.md](docs/references.md) | 借鉴、改造或放弃的外部做法 |
| [docs/README.md](docs/README.md) | `docs/` 下所有页面的索引 |

以上文档均为英文。

## 阅读地图

想弄清某个问题时从哪里读起。每个入口都写成「包 + 符号」，包内文件拆分后依然有效。

| 问题 | 从这里读 |
| --- | --- |
| 进程是怎么组装起来的？ | `internal/server#Run` |
| 有哪些 HTTP 路由？ | `internal/api#Server.Routes` |
| API 到底接收和返回什么？ | `internal/api/openapi.yaml` |
| 作业如何排队、运行、取消和重试？ | `internal/jobs#Manager` |
| 一首曲目如何下载、解密、重封装和打标签？ | `internal/media#Downloader` |
| 哪个 Apple 主机响应请求？ | `internal/applemusic#ampAPIBase`、`internal/applemusic#CatalogClient.doWithWebAuth` |
| amdl 怎么和 wrapper 通信？ | `internal/wrapper#Client` |
| 样本怎么解密？ | `internal/fairplay#Template` |
| 作业存在哪里，表结构如何迁移？ | `internal/db#Store` |
| 配置如何加载和校验？ | `internal/config#Load`、`internal/config#Config.Validate` |
| 作业钩子发送什么？ | `internal/hooks#Dispatcher` |
| 资料库同步如何挑选专辑？ | `internal/librarysync#Watcher` |
| 网页控制台怎么提供？ | `apps/console#Handler` |
| 有哪些命令和 MCP 工具？ | `internal/capability#All`、`apps/amdl/internal/cli#Run`、`apps/amdl/internal/mcpserver#New` |
| 终端界面怎么工作？ | `apps/amdl/internal/tui#Run` |
| 客户端怎么调用 API？ | `internal/client#Client` |
| 客户端怎么在本机运行后端？ | `internal/local#Backend` |
| amdl 怎么在 QEMU 里运行 wrapper？ | `internal/wrapperhost#Supervisor` |
| 官网怎么构建？ | `apps/site/build.mjs#renderPage` |

## 开发

需要 Go（版本见 [`go.mod`](go.mod)）、`PATH` 上的 `ffmpeg`，以及运行脚本测试用的 Node.js 26。

| 命令 | 作用 |
| --- | --- |
| `make check` | CI 门禁：gofmt 检查、构建、vet、测试、竞态检测、`govulncheck`、Node 脚本测试 |
| `make test`、`make race` | 测试套件，不带或带竞态检测 |
| `make dev` | 以模拟模式在 `127.0.0.1:18080` 启动 API，配置和数据库是 `data/dev/` 下的临时文件 |
| `make docker` | 在本地构建容器镜像 |
| `node apps/site/build.mjs --serve 8080` | 构建官网，并在 `127.0.0.1:8080` 上预览 |
| `make help` | 列出所有目标 |

`make dev` 不需要 wrapper：模拟模式读取真实的曲库数据、选择真实的媒体，然后模拟传输、解密和
写文件，同时发布和真实下载相同的状态与事件。

CI 在每次推送到 `main` 和 `dev` 以及每个拉取请求上运行 `make check` 的各个步骤，并构建 Docker
镜像。开发在 `dev` 上进行；`main` 只通过发布用的拉取请求变更。每个提交都需要 DCO 签名
（`git commit -s`），见 [CONTRIBUTING.md](CONTRIBUTING.md)。编码 agent 从 [AGENTS.md](AGENTS.md) 读起。

## 许可证

[AGPL-3.0](LICENSE)。

`internal/fairplay` 翻译自 [Temari](https://github.com/WorldObservationLog/Temari)，后者采用
MIT 许可证；其许可声明见 `internal/fairplay/doc.go`。
