# AMDL Backend

AMDL Backend 是 Apple Music 下载系统的核心后端服务。它负责解析 Apple Music 单曲、专辑、歌单、艺术家和电台链接，调度下载任务，对接 `wrapper-manager` 获取媒体数据，并通过 HTTP API 与 SSE 暴露任务状态。

当前仓库以根目录 Go 模块为生产代码来源，主要代码位于 `cmd/`、`internal/`、`configs/` 等目录。

## 功能

- 支持 Apple Music 单曲、专辑、歌单、艺术家和电台 URL。
- 不会支持 Apple Music MV 下载；受 L3 限制，当前链路只能获取低分辨率视频，不符合本项目的下载质量目标。
- 通过 `wrapper-manager` gRPC 获取账号状态、播放清单和媒体数据。
- 使用 SQLite 持久化任务、任务项和事件。
- 通过 SSE 或 WebSocket 推送下载进度，支持单任务订阅与跨任务总览 feed。
- 支持 Enhanced HLS 编码回退链和 AAC-LC 保底格式。
- 支持歌词嵌入、歌词边车文件、封面嵌入和独立封面保存。
- 支持任务生命周期 hooks（`configs/hooks.yaml`）：在任务排队或进入终态时触发 webhook 或本地命令。
- 提供结构化日志、敏感字段脱敏、请求/任务关联、可选压缩轮转文件，以及带过滤和断线续接的日志查询/SSE API。
- 支持本地模拟（simulate）模式：不实际下载/解密，用于联调和压测下载流水线（配置文件顶层 `simulate` 段）。
- 提供 Swagger UI 与 OpenAPI 3.1 规范。
- 使用 GitHub Actions 在发版时自动生成 Release changelog。

## 依赖

- Go 版本以 `go.mod` 为准。
- 可访问的 `wrapper-manager` 服务。
- 媒体封装阶段需要以下外部命令：
  - `ffmpeg`（用于重封装扁平化与可选的完整性校验）

  > 样本抽取、重封装、元数据与封面写入均已改为进程内的 Go 库实现（`mp4ff` / `go-mp4tag`），不再依赖 `gpac`、`MP4Box`、`mp4extract`、`mp4edit`。

## 快速启动

```bash
go run ./cmd/amdl-api
```

默认配置文件为：

```text
configs/config.yaml
```

首次启动时该文件会自动以示例 `configs/config.example.yaml` 的值为模板
生成（只取配置值，不带注释；字段文档都在示例文件里）。`config.yaml`
本身由后端管理：`PUT /api/v1/config` 修改运行时配置时会整体重写它，
因此它不纳入版本控制；手工编辑仍然可以，重启后生效，但会在下一次
API 修改时被重写。

启动前请按实际环境修改 `configs/config.yaml`（或先改示例文件再首次启动），尤其是：

- `server.listen`：API 监听地址。
- `wrapper.address`：`wrapper-manager` gRPC 地址。
- `database.path`：SQLite 数据库路径（默认 `data/db/amdl.db`）。
- `logging.*`：日志级别、格式、内存保留和可选轮转文件（字段完整说明见示例配置）。
- `download.downloads_dir`：下载文件保存目录。
- `tools.*`：外部媒体工具命令路径或命令名。

### 环境变量覆盖

任何配置项都可以用环境变量覆盖，变量名为 `AMDL_<大写段名>_<大写键名>`，
例如 `AMDL_SERVER_LISTEN`、`AMDL_WRAPPER_ADDRESS`、`AMDL_DATABASE_PATH`、
`AMDL_LOGGING_LEVEL`、
`AMDL_DOWNLOAD_QUALITY_PRIORITY`。规则：

- 环境变量优先于 `config.yaml`，每次启动（以及每次配置重载）都生效，加载
  时不会写回文件——取消设置后下次启动即恢复文件里的值（注意
  `PUT /api/v1/config` 重写整份文件时，写入的是包含环境变量在内的当前
  生效值）。
- 值的写法：字符串原样填写；布尔用 `true`/`false`；整数直接写数字；
  字符串列表用逗号分隔（如 `alac,aac`），空值表示空列表。
- 无法识别的 `AMDL_*` 变量会让启动失败，避免拼写错误被静默忽略
  （`AMDL_CONFIG`、`AMDL_HOOKS_CONFIG` 除外）。
- 被环境变量覆盖的字段无法再通过 `PUT /api/v1/config` 修改（返回 422），
  请改环境变量并重启。

## Docker 部署

仓库根目录提供 `Dockerfile` 与 `docker-compose.yml`。发版时 GitHub Actions 会自动构建多架构镜像（linux/amd64 + linux/arm64）并推送到 GHCR（镜像 tag 与版本对应，如 `v1.2.3` → `1.2.3`、`1.2`、`latest`），`docker-compose.yml` 默认就直接拉取该镜像，无需本地构建：

```bash
docker compose up -d
```

> 匿名拉取要求 GHCR 上的镜像 package 为 public。本仓库已公开;若你 fork 自建,首次推送到 GHCR 时 package 默认是私有的,`docker compose up -d` 会以 `unauthorized`/`denied` 失败——到你仓库的 Packages 设置里改成 public 即可(详见下方[发版](#发版)小节),或改用本地构建。

想固定版本，把 compose 里的 `:latest` 换成具体 tag（如 `:1.1`）。

若想从源码本地构建（镜像为多阶段构建：构建阶段产出静态二进制——纯 Go SQLite，无 CGO；运行阶段基于 Alpine 并内置 `ffmpeg`。容器以 root 启动入口脚本，完成配置播种和挂载目录属主修正后，通过 `su-exec` 降权到 `PUID:PGID`（默认 `1000:1000`）运行后端进程），取消 `docker-compose.yml` 里 `build: .` 的注释后：

```bash
docker compose up -d --build
```

或者不用 compose：

```bash
docker build -t amdl-backend .
docker run -d --name amdl-backend \
  -p 18080:18080 \
  -v ./configs:/app/configs \
  -v ./data/db:/app/data/db \
  -v ./data/logs:/app/data/logs \
  -v ./data/downloads:/app/data/downloads \
  --add-host host.docker.internal:host-gateway \
  amdl-backend
```

### 配置播种

首次启动时入口脚本会把镜像内置的示例配置播种到配置目录 `/app/configs`：

- `config.example.yaml`：原样复制（后端的 bootstrap 逻辑要求它与 `config.yaml` 同目录，同时充当字段文档）。
- `hooks.yaml`：复制注释完整的模板（默认禁用）。

`config.yaml` 由后端自己在首次启动时从示例生成（只取配置值，不带注释，与 `PUT /api/v1/config` 重写后的机器管理格式一致）。两个容器内不可用的仓库默认值不再写进文件，而是由入口脚本通过环境变量覆盖机制在每次启动时改写：`server.listen` 覆盖为 `AMDL_SERVER_LISTEN`（默认 `:18080`，容器内必须监听非回环地址），`wrapper.address` 覆盖为 `AMDL_WRAPPER_ADDRESS`（默认 `host.docker.internal:8080`）。

已存在的文件永远不会被覆盖，因此 `PUT /api/v1/config` 写回的配置在容器重建后保持不变。

### 环境变量

任何配置项都可以用 `AMDL_<大写段名>_<大写键名>` 环境变量覆盖（见上文
[环境变量覆盖](#环境变量覆盖)），例如 `AMDL_DOWNLOAD_QUALITY_PRIORITY: "alac,aac"`、
`AMDL_SIMULATE_ENABLED: "true"`。容器相关的其它变量：

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `PUID` | `1000` | 后端进程的运行用户 UID。每次启动生效：配置目录每次整体修正属主；数据目录仅在顶层属主不匹配时递归修正一次（首次绑定挂载或变更 `PUID`/`PGID` 后的启动可能因此稍慢）。 |
| `PGID` | `1000` | 后端进程的运行用户 GID。行为同 `PUID`。 |
| `TZ` | UTC | 容器时区（IANA 名称，如 `Asia/Shanghai`），影响日志时间戳与 exec hooks 命令看到的本地时间。镜像已内置 tzdata，Go 运行时自动识别。 |
| `AMDL_SERVER_LISTEN` | `:18080`（入口脚本的容器默认值） | API 监听地址，每次启动覆盖 `server.listen`。健康检查端口也从它推导。 |
| `AMDL_WRAPPER_ADDRESS` | `host.docker.internal:8080`（入口脚本的容器默认值） | `wrapper-manager` gRPC 地址，每次启动覆盖 `wrapper.address`。 |
| `AMDL_CONFIG` | `/app/configs/config.yaml` | 主配置文件路径（后端原生支持）。 |
| `AMDL_HOOKS_CONFIG` | `/app/configs/hooks.yaml` | hooks 配置文件路径（后端原生支持）。 |

修改配置的三种方式：运行期字段用 `PUT /api/v1/config`，立即生效；任意字段用环境变量覆盖后 `docker compose up -d`；或编辑挂载目录里的 `config.yaml` 后重启容器（启动期字段需重启，且监听地址与 wrapper 地址在容器内始终以环境变量为准）。

> **破坏性变更**：旧变量名 `AMDL_LISTEN`、`AMDL_WRAPPER_ADDR` 已移除。仍设置它们会因「未知配置环境变量」导致启动失败，请改用 `AMDL_SERVER_LISTEN`、`AMDL_WRAPPER_ADDRESS`。

如果偏好完全不以 root 启动容器，也可以用 `docker run --user <uid>:<gid>`（或 compose 的 `user:`）直接指定运行用户：此时入口脚本跳过降权与属主修正，忽略 `PUID`/`PGID`，挂载目录对该用户可写需自行保证。

### 挂载目录

`docker-compose.yml` 默认把四个目录绑定挂载到宿主机：

- `./configs` → `/app/configs`：配置目录（`config.yaml`、`config.example.yaml`、`hooks.yaml`）。
- `./data/db` → `/app/data/db`：SQLite 数据库目录（`database.path` 默认 `data/db/amdl.db`）。
- `./data/logs` → `/app/data/logs`：轮转日志目录；仅在 `logging.file_enabled: true` 时写入。
- `./data/downloads` → `/app/data/downloads`：下载产物目录。想放到宿主机其它位置，改冒号左边的宿主机路径即可（例如 `/path/to/music:/app/data/downloads`）。
- 临时目录 `data/tmp` 默认留在容器内，无需挂载。它承担下载/解密/转封装/校验/打标签这几步的落盘中转（见 `configs/config.example.yaml` 里 `download.temp_dir` 的注释），如果容器存储驱动较慢，可以把它单独挂载到宿主机的快速磁盘（如 SSD）上，与 `data/downloads` 分开。
- 启用开发者令牌签名时，把 `.p8` 私钥以只读方式挂载进容器（例如 `-v ./keys:/app/keys:ro`），并将 `catalog.apple_music_private_key_path` 指向容器内路径。密钥不会也不应打进镜像（`.dockerignore` 已排除 `keys/` 与 `*.p8`）。
- 把 `PUID`/`PGID` 设成宿主机目录属主的 uid/gid 即可，入口脚本会自动修正容器内挂载目录的属主。

### wrapper-manager 地址

- wrapper 跑在宿主机：保持默认 `host.docker.internal:8080`（compose 文件已通过 `extra_hosts: host-gateway` 让 Linux 容器也能解析该域名）。
- wrapper 也是 compose 服务：将 `AMDL_WRAPPER_ADDRESS` 设为 `<服务名>:8080`。
- gRPC 连接是懒建立的，后端启动不要求 wrapper 在线；可用 `GET /api/v1/wrapper/status` 验证连通性。

容器健康检查使用 `GET /api/v1/health`。

## API

以下示例假设服务监听在 `http://localhost:18080`。

交互式 Swagger UI：

```text
http://localhost:18080/docs
```

OpenAPI 3.1 规范：

```text
http://localhost:18080/api/openapi.yaml
```

读取运行时配置：

```bash
curl http://localhost:18080/api/v1/config
```

查询最近的错误日志：

```bash
curl 'http://localhost:18080/api/v1/logs?level=error&limit=100'
```

按任务过滤并实时订阅日志（SSE `id` 可作为重连时的 `Last-Event-ID`）：

```bash
curl -N 'http://localhost:18080/api/v1/logs/stream?job_id=job_01JZ0000000000000000000000'
```

每个 HTTP 响应都带 `X-Request-ID`。调用方也可以在请求中传入同名头，随后用
`GET /api/v1/logs?request_id=<id>` 聚合同一请求的访问日志与同步任务操作日志。
日志 API 的内存保留量由 `logging.buffer_size` 控制；设为 `0` 时不保留历史，
实时 SSE 仍会推送新记录。文件输出默认关闭，启用后按 `max_size_mb`、
`max_backups`、`max_age_days` 自动轮转，并可用 `compress` 压缩旧文件。
`logging.level` 与 `logging.access_log` 可通过 `PUT /api/v1/config` 即时调整；
格式、输出目标、缓冲容量和轮转参数修改后需要重启。

检查 `wrapper-manager` 状态：

```bash
curl http://localhost:18080/api/v1/wrapper/status
```

登录 wrapper 账号：

```bash
curl -X POST http://localhost:18080/api/v1/wrapper/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"apple-id@example.com","password":"password"}'
```

如果响应包含 `"status":"needs_2fa"`，使用响应中的 `login_id` 在 30 秒内提交验证码：

```bash
curl -X POST http://localhost:18080/api/v1/wrapper/login/{login_id}/2fa \
  -H 'Content-Type: application/json' \
  -d '{"two_step_code":"123456"}'
```

登出 wrapper 账号：

```bash
curl -X POST http://localhost:18080/api/v1/wrapper/logout \
  -H 'Content-Type: application/json' \
  -d '{"username":"apple-id@example.com"}'
```

创建下载任务：

```bash
curl -X POST http://localhost:18080/api/v1/downloads \
  -H 'Content-Type: application/json' \
  -d '{"url":"https://music.apple.com/us/album/example/123456789?i=987654321","force":true}'
```

`force: true` 会覆盖已存在的音频及其歌词边车文件；默认为 `false`，已存在的文件会被跳过。

下载电台（station，链接形如 `https://music.apple.com/us/station/.../ra.xxxx`）：仅支持能解析为曲目列表的个性化/精选电台，需提供订阅令牌 `media_user_token`（可随请求提供，也可写入 `catalog.media_user_token`，并用 `catalog.media_user_token_priority` 选择请求值或配置值优先）；直播电台（Apple Music 1 等）没有静态曲目列表，任务会以明确错误结束。电台曲目取自 Apple Music 的「接下来播放」滚动列表，因此一次下载捕获的是当前返回的若干首曲目，而非固定编目。随请求提供的 `media_user_token` 只保存在内存中、随这批任务的生命周期存在；写入配置文件的令牌会随配置持久化。该令牌同时用于私人歌单（`pl.u-xxx`）的封面获取：公共目录不含私人歌单封面，提供令牌后会以用户身份从库副本读取；不提供令牌时私人歌单仍可下载，只是没有歌单封面。电台产物存入独立的电台目录，按 `download.station_path_format`（默认 `stations/{StationName}/{SongNumber:02d}. {SongName}`）归档。

```bash
curl -X POST http://localhost:18080/api/v1/downloads \
  -H 'Content-Type: application/json' \
  -d '{"urls":["https://music.apple.com/us/station/example/ra.978194965"],"media_user_token":"<你的 media-user-token>"}'
```

查询任务：

```bash
curl http://localhost:18080/api/v1/downloads/{job_id}
```

列出任务（支持分页与筛选）：

```bash
curl 'http://localhost:18080/api/v1/downloads?limit=20&offset=0&status=failed,cancelled&type=album&storefront=cn&q=beta&created_after=2024-07-01&sort=updated_at&order=desc'
```

可用查询参数：`limit`、`offset`、`status`、`type`、`storefront`、`q`、`created_after`、`created_before`、`updated_after`、`updated_before`、`sort`、`order`。响应额外返回 `total`、`limit`、`offset`。

监听任务事件：

```bash
curl -N http://localhost:18080/api/v1/downloads/{job_id}/events
```

取消任务：

```bash
curl -X POST http://localhost:18080/api/v1/downloads/{job_id}/cancel
```

重试失败任务（仅 `failed` 状态的任务可重试；非失败状态、仍在收尾上一次运行、或已有同 key 任务在跑时返回 409）：

```bash
curl -X POST http://localhost:18080/api/v1/downloads/{job_id}/retry
```

删除已结束（终态）的任务及其记录：

```bash
curl -X DELETE http://localhost:18080/api/v1/downloads/{job_id}
```

任务事件也可通过 WebSocket 订阅（`GET /api/v1/downloads/{job_id}/events/ws`），与上面的 SSE 端点等价，供偏好 WS 的客户端使用。

其它端点（详细请求/响应结构见 Swagger UI）：

- `GET /api/v1/downloads/events`（及 `/events/ws`）：跨任务的总览 feed，推送任务列表增删改，无需分别订阅每个任务。
- `POST /api/v1/quality`：不创建任务，仅探测某个 URL 当前可用的编码与画质信息。
- `GET /api/v1/developer-token`：签发可共享的 Apple Music developer token；仅在启用本地签名模式（`catalog.apple_music_*` 三个 key 配置齐全）时可用，否则返回 409。

## 下载行为

仅音频下载是受支持目标。Apple Music MV 下载不会支持，因为 L3 限制下只能获取低分辨率视频。

### 重试与编码降级

- `download.max_attempts`：元数据、封面、歌词以及每个编码的下载/解密阶段的最大总尝试次数（含首次）；正数允许 `1-10`。例如 `4` 表示每个操作最多尝试 4 次；值 `<= 0` 仍按 1 处理（仅尝试一次，不重试）。
- `download.quality_priority`：按顺序尝试的 Enhanced HLS 编码回退链，支持 `alac`、`aac`、`aac-binaural`、`aac-downmix`、`ec3` 和 `ac3`。
- `download.codec_alternative`：是否在前一个编码重试耗尽后继续尝试回退链；关闭时只尝试第一个编码。
- `aac-lc` 无需写入 `quality_priority`；开启编码回退时会自动追加为最后的 WebPlayback 保底格式。
- 回退链中的每个编码（含隐式 AAC-LC 保底）均使用 `download.max_attempts`；每个编码的下载阶段和解密阶段分别独立计数重试。
- 重试、耗尽、恢复和编码回退会通过任务 SSE 事件返回；任务详情中的每个项目也会返回 `retry_kind`、`attempt`、`max_attempts` 和 `status_message`，其中 `attempt` 为当前阶段（`retry_kind`）的尝试序号（从 1 开始）。

### 歌词

- `download.embed_lyrics` 控制是否写入 MP4 歌词标签。
- `download.save_lyrics_file` 控制是否保存 `.lrc` 或 `.ttml` 边车文件。
- `download.lyrics_format` 支持 `lrc` 和 `ttml`；`ttml` 会保留 wrapper 返回的原始 TTML。
- `download.lyrics_type` 支持 `lyrics` 和 `syllable-lyrics`；后者用于请求 Apple Music 逐词歌词。
- `download.lyrics_extras` 可配置 `translation`、`pronunciation`，仅在 `lyrics_format: "lrc"` 时参与转换。
- 歌词、逐词歌词、翻译和音译需要 `wrapper-manager` 具备有效 Apple Music 订阅登录态。
- 歌词获取或转换失败不会中断音频下载；后端会继续保存无歌词音频并在任务项状态中说明原因。

## 保存格式

每种任务类型的完整保存路径由一行模板配置，相对 `download.downloads_dir` 解析，末段为文件名（自动追加 `.m4a` 扩展名）：

- `download.song_path_format`，默认 `songs/{ArtistName}/{AlbumName}/{TrackNumber:02d}. {SongName}`
- `download.album_path_format`，默认 `albums/{ArtistName}/{AlbumName}/{TrackNumber:02d}. {SongName}`
- `download.artist_path_format`，默认 `artists/{ArtistName}/{AlbumName}/{TrackNumber:02d}. {SongName}`（艺术家任务会展开为该艺术家的专辑/单曲列表）
- `download.playlist_path_format`，默认 `playlists/{PlaylistName}/{SongNumber:02d}. {SongName}`（`{SongNumber}` 为歌曲在歌单中的序号）
- `download.station_path_format`，默认 `stations/{StationName}/{SongNumber:02d}. {SongName}`（电台任务；`{StationName}`/`{StationId}` 为电台名/ID）

以默认配置为例，专辑曲目会保存到：

```text
data/downloads/albums/{ArtistName}/{AlbumName}/{TrackNumber:02d}. {SongName}.m4a
```

模板变量列表见 `configs/config.example.yaml` 注释。目录段中的 `{ArtistName}` 使用集合的归档艺术家（优先专辑艺术家），保证同一专辑的曲目落在同一目录；文件名段使用曲目自身的艺术家。

可选择在音频文件外额外保存独立封面：

- `download.save_album_cover: true`：在专辑目录保存 `cover.jpg` 或 `cover.png`。
- `download.save_artist_cover: true`：在艺术家目录保存 `artist.jpg` 或 `artist.png`。
- `download.save_playlist_cover: true`：在歌单目录保存 `cover.jpg` 或 `cover.png`。

封面目录按路径模板中的变量定位：专辑封面写入引用 `{AlbumName}`/`{AlbumId}` 的最深目录段（若无则写入音频文件所在目录）；艺术家封面写入引用艺术家变量（`{ArtistName}`、`{UrlArtistName}`、`{AlbumArtist}`、`{ArtistId}`）的最深目录段，若模板目录中没有艺术家段则跳过艺术家封面。文件扩展名跟随 `download.cover_format`。歌单与电台为平铺目录，可保存歌单/电台封面，但不会额外写入专辑或艺术家封面。

对于带 `?i=<song_id>` 的专辑链接，可通过 `catalog.album_track_url_mode` 选择任务类型：

- `song`（默认）：视为单曲任务，使用 `i` 参数中的歌曲 ID。
- `album`：忽略 `i` 参数，视为专辑任务并下载整张专辑。

## 测试

```bash
go test ./...
```

如需强制绕过 Go 测试缓存：

```bash
go test ./... -count=1
```

## 发版

手动运行 `Release` workflow（`workflow_dispatch`，指定版本号）时，GitHub Actions 会先执行完整 Go 测试，再创建 GitHub Release；本仓库不通过推送 tag 触发发版。

Release changelog 由 GitHub generated release notes 自动生成，分类规则位于：

```text
.github/release.yml
```

发版成功后 `Release` workflow 会调用 `Docker Publish` workflow，构建多架构镜像（linux/amd64 + linux/arm64）并推送到 `ghcr.io/amdl-web/apple-music-downloader-backend`，镜像 tag 为 `{version}`、`{major}.{minor}` 与 `latest`。`Docker Publish` 也可对已存在的版本标签手动触发（补发或重发镜像），在 GitHub 页面手工发布 Release 时同样会自动运行。手动触发默认不移动 `latest`（避免重发旧版本时把 `latest` 拉回去），仅在勾选 `latest` 选项时才更新；页面手工发布的预发布（prerelease）Release 也不会移动 `latest`。首次推送会在 GHCR 创建私有 package，如需公开拉取，请到仓库 Packages 设置里将其改为 public。
