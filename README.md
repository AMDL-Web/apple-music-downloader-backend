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
- 使用 GitHub Actions 发版，支持仓库内版本说明并在缺失时自动生成 Release changelog。

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

配置拆分为两个文件（可分别用 `AMDL_CONFIG`、`AMDL_RUNTIME_CONFIG` 指定路径）：

```text
configs/config.yaml    # 启动配置：仅进程启动时读取，改动需重启生效
configs/runtime.yaml   # 运行时配置：PUT /api/v1/config 可在线修改
```

首次启动时两个文件分别以 `configs/config.example.yaml` 和
`configs/runtime.example.yaml` 为模板自动生成（字段文档都在示例文件里）。
`config.yaml` 归使用者所有：后端不会重写它，注释和格式可长期保留。
`runtime.yaml` 由后端管理：`PUT /api/v1/config` 修改运行时配置时会整体
重写它（注释不保留），因此它不纳入版本控制；手工编辑仍然可以，下一次
`GET /api/v1/config` 后即生效、无需重启，但会在下一次 API 修改时被重写。

从旧版单文件 `config.yaml` 升级时，文件拆分本身无需手工操作：首次启动
会自动把运行时字段拆到 `runtime.yaml`、把 `config.yaml` 重写为仅启动字
段，并在旁边留一份 `config.yaml.pre-split.bak` 备份。但若旧配置里还有
已删除的键（如 `download.max_parallel_tracks` 等按任务并发键），启动会
以明确的未知字段错误拒绝加载，需要先手工删掉这些键再启动（见
[下载行为](#重试与编码降级)一节的破坏性变更说明）。

启动前请按实际环境修改 `configs/config.yaml`（或先改示例文件再首次启动），尤其是：

- `server.listen`：API 监听地址。
- `wrapper.address`：`wrapper-manager` gRPC 地址。
- `database.path`：SQLite 数据库路径（默认 `data/db/amdl.db`）。
- `logging.*`：日志格式、内存保留和可选轮转文件（级别与访问日志开关在 `runtime.yaml`；字段完整说明见示例配置）。
- `tools.*`：外部媒体工具命令路径或命令名。

下载相关选项（如 `download.downloads_dir` 下载保存目录）在 `runtime.yaml`，
可随时通过 `PUT /api/v1/config` 或编辑文件调整。

### 环境变量覆盖

任何配置项都可以用环境变量覆盖，变量名为 `AMDL_<大写段名>_<大写键名>`，
例如 `AMDL_SERVER_LISTEN`、`AMDL_WRAPPER_ADDRESS`、`AMDL_DATABASE_PATH`、
`AMDL_LOGGING_LEVEL`、
`AMDL_DOWNLOAD_QUALITY_PRIORITY`。规则：

- 环境变量优先于两个配置文件，每次启动（以及每次配置重载）都生效，加载
  时不会写回文件——取消设置后下次启动即恢复文件里的值（注意
  `PUT /api/v1/config` 重写 `runtime.yaml` 时，写入的是包含环境变量在内
  的当前生效值）。
- 值的写法：字符串原样填写；布尔用 `true`/`false`；整数直接写数字；
  字符串列表用逗号分隔（如 `alac,aac`），空值表示空列表。
- 无法识别的 `AMDL_*` 变量会让启动失败，避免拼写错误被静默忽略
  （`AMDL_CONFIG`、`AMDL_RUNTIME_CONFIG`、`AMDL_HOOKS_CONFIG` 除外）。
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

- `config.example.yaml`、`runtime.example.yaml`：原样复制（后端的 bootstrap 逻辑要求它们与 `config.yaml` 同目录，同时充当字段文档）。
- `hooks.yaml`：复制注释完整的模板（默认禁用）。

`config.yaml`（启动配置）与 `runtime.yaml`（运行时配置）由后端自己在首次启动时从各自示例生成；旧版单文件 `config.yaml` 会被自动拆分迁移并留下备份。两个容器内不可用的仓库默认值不再写进文件，而是由入口脚本通过环境变量覆盖机制在每次启动时改写：`server.listen` 覆盖为 `AMDL_SERVER_LISTEN`（默认 `:18080`，容器内必须监听非回环地址），`wrapper.address` 覆盖为 `AMDL_WRAPPER_ADDRESS`（默认 `host.docker.internal:8080`）。

已存在的文件永远不会被覆盖，因此 `PUT /api/v1/config` 写回的运行时配置在容器重建后保持不变。

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
| `AMDL_CONFIG` | `/app/configs/config.yaml` | 启动配置文件路径（后端原生支持）。 |
| `AMDL_RUNTIME_CONFIG` | 与启动配置同目录的 `runtime.yaml` | 运行时配置文件路径（后端原生支持）。 |
| `AMDL_HOOKS_CONFIG` | `/app/configs/hooks.yaml` | hooks 配置文件路径（后端原生支持）。 |

修改配置的三种方式：运行期字段用 `PUT /api/v1/config`，立即生效；任意字段用环境变量覆盖后 `docker compose up -d`；或编辑挂载目录里的配置文件后重启容器——运行期字段改 `runtime.yaml`（下一次 `GET /api/v1/config` 即生效，无需重启），启动期字段改 `config.yaml` 并重启（监听地址与 wrapper 地址在容器内始终以环境变量为准）。

> **破坏性变更**：旧变量名 `AMDL_LISTEN`、`AMDL_WRAPPER_ADDR` 已移除。仍设置它们会因「未知配置环境变量」导致启动失败，请改用 `AMDL_SERVER_LISTEN`、`AMDL_WRAPPER_ADDRESS`。

如果偏好完全不以 root 启动容器，也可以用 `docker run --user <uid>:<gid>`（或 compose 的 `user:`）直接指定运行用户：此时入口脚本跳过降权与属主修正，忽略 `PUID`/`PGID`，挂载目录对该用户可写需自行保证。

### 挂载目录

`docker-compose.yml` 默认把四个目录绑定挂载到宿主机：

- `./configs` → `/app/configs`：配置目录（`config.yaml`、`runtime.yaml`、两个示例文件、`hooks.yaml`）。
- `./data/db` → `/app/data/db`：SQLite 数据库目录（`database.path` 默认 `data/db/amdl.db`）。
- `./data/logs` → `/app/data/logs`：轮转日志目录；仅在 `logging.file_enabled: true` 时写入。
- `./data/downloads` → `/app/data/downloads`：下载产物目录。想放到宿主机其它位置，改冒号左边的宿主机路径即可（例如 `/path/to/music:/app/data/downloads`）。
- 临时目录 `data/tmp` 默认留在容器内，无需挂载。它承担下载/解密/转封装/校验/打标签这几步的落盘中转（见 `configs/runtime.example.yaml` 里 `download.temp_dir` 的注释），如果容器存储驱动较慢，可以把它单独挂载到宿主机的快速磁盘（如 SSD）上，与 `data/downloads` 分开。
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

下载电台（station，链接形如 `https://music.apple.com/us/station/.../ra.xxxx`）：仅支持能解析为曲目列表的个性化/精选电台，需提供 Apple Music 订阅令牌（media-user-token）。按任务入口是 `overrides.media_user_token`；未提供该覆盖时，后端使用运行时配置中的 `catalog.media_user_token` 作为 fallback。覆盖字段具有三态语义：省略表示沿用全局 fallback，非空字符串表示本批任务使用该值，显式空字符串 `""` 表示为本批任务清空全局 fallback。旧的请求顶层 `media_user_token` 不再接受，会作为未知字段返回 `400`。`catalog.media_user_token_priority` 只为兼容旧配置保留，现已弃用且不再参与选择。

请求覆盖令牌只会持久化到实际需要它的电台任务和私人歌单（`pl.u-xxx`）任务；同一批中的单曲、专辑、艺人和其它歌单任务不会保存它。任务完成或取消后令牌会被清除；失败任务会保留令牌，以便后续重试继续解析。创建、列表、详情以及 SSE/WebSocket 等任务响应永不回显令牌。写入 `catalog.media_user_token` 的全局 fallback 则随配置文件持久化，并可能由 `GET /api/v1/config` 返回。该令牌还用于私人歌单的封面获取：公共目录不含私人歌单封面，提供令牌后会以用户身份从库副本读取；不提供令牌时私人歌单仍可下载，只是没有歌单封面。

直播电台（Apple Music 1 等）没有静态曲目列表，任务会以明确错误结束。电台曲目取自 Apple Music 的「接下来播放」滚动列表，因此一次下载捕获的是当前返回的若干首曲目，而非固定编目。电台产物存入独立的电台目录，按 `download.station_path_format`（默认 `stations/{StationName}/{SongNumber:02d}. {SongName}`）归档。

```bash
curl -X POST http://localhost:18080/api/v1/downloads \
  -H 'Content-Type: application/json' \
  -d '{"urls":["https://music.apple.com/us/station/example/ra.978194965"],"overrides":{"media_user_token":"<你的 media-user-token>"}}'
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
- `POST /api/v1/quality`：不创建任务，探测单曲、专辑、歌单、艺人或电台 URL 的 master playlist 当前声明的编码与音质信息；它复用下载任务的区域校验、集合解析、元数据刷新、重试、并发调度、HLS 来源选择及 codec/ALAC 筛选规则。正常成功路径为每个曲目出现项读取一次 master playlist 并从中汇总全部音质；失败时按 `download.max_attempts` 重新取得来源并读取 master。查询不读取具体 media playlist，也不验证媒体分片或进入加密媒体传输。区域校验要求 wrapper/decryptor 已就绪，且 URL storefront 位于其上报的 `regions` 中。所有链接类型统一返回逐曲 `tracks`；单曲包含一个元素，集合保持 Apple Music 原顺序和重复曲目出现项。电台使用运行时 `catalog.media_user_token`。
- `GET /api/v1/developer-token`：签发可共享的 Apple Music developer token；仅在启用本地签名模式（`catalog.apple_music_*` 三个 key 配置齐全）时可用，否则返回 409。

## 下载行为

仅音频下载是受支持目标。Apple Music MV 下载不会支持，因为 L3 限制下只能获取低分辨率视频。

### 重试与编码降级

- `download.max_parallel_downloads` 和 `download.max_parallel_decrypts` 分别限制单个 backend 进程内、跨全部任务共享的加密媒体下载与解密阶段，默认 `16` 和 `32`；已下载但尚未完成解密的媒体还受内部 in-flight 背压保护。`download.max_parallel_wrapper_requests`（默认 `24`）限制 wrapper-manager 的数据类 RPC（M3U8、歌词、web playback、license）的进程级并发，登录/登出不受限，解密流由 `max_parallel_decrypts` 约束。旧 `download.max_parallel_tracks` 等按任务键已删除，升级时必须手工迁移配置。
- `catalog.max_parallel_requests`（默认 `16`）限制 Catalog API、web token、封面和 HLS 清单等 Apple 小请求的进程级并发；认证 Catalog/amp-api 请求另受 `catalog.requests_per_second`（默认 `10`）和 `catalog.request_burst`（默认 `16`）约束。Apple 返回 429 时会遵循 `Retry-After`、触发全局冷却并自动重试一次。这五个并发/速率值均在启动时固定，修改配置后需要重启；多个 backend 副本之间不共享槽位。
- 多任务争抢池容量时按任务提交时间分配：最早提交且未完成的任务优先拿到许可，因此任务倾向于逐个完成而非交错推进；被恢复的任务保留原提交时间、重启后不丢排位。优先级只在池满时起作用——前面的任务喂不满池子时，空闲许可立即分给后续任务，不浪费吞吐。URL 校验、音质探测等交互式 API 请求不参与任务排队，始终优先放行。
- `download.max_attempts`：元数据、封面、歌词以及每个编码的下载/解密阶段的最大总尝试次数（含首次）；正数允许 `1-10`。例如 `4` 表示每个操作最多尝试 4 次；值 `<= 0` 仍按 1 处理（仅尝试一次，不重试）。
- 可重试错误使用带随机抖动的指数退避；Apple Catalog 返回 `Retry-After` 时，等待时间不会短于该提示，避免同一批请求同步重放。
- `download.quality_priority`：按顺序尝试的 Enhanced HLS 编码回退链，支持 `alac`、`aac`、`aac-binaural`、`aac-downmix`、`ec3` 和 `ac3`。
- `download.memory_mode`：控制 Enhanced HLS 路径的内存/磁盘取舍。两种模式现在都会逐片段解密并直接送入 ffmpeg，不再落盘整轨明文 `dec-*`：`low`（默认）从可续传的加密 `raw-*` 检查点读取，只有片段级内存，但临时目录会同时存在加密检查点和重封装输出；`high` 将一份加密整轨保留在内存，只在临时目录保留重封装输出。High 在已取得可用于 `If-Range` 的 ETag 或 Last-Modified 时，可对一次 CDN 中途断流执行纯内存 `Range` 重连，但不持久化断点，进程或容器重启后仍从零开始。高模式的单轨内存媒体上限为 512 MiB；由于 Go GC 会保留分配余量，实际堆峰值可能接近整轨大小的两倍，进程内存和临时盘占用也会随任务及曲目并发数放大。AAC-LC 回退不受此选项影响：加密重试数据仍保留在内存，Widevine 解密结果直接流入 ffmpeg，不再额外保留整轨明文 `[]byte` 或输入文件；当前 gowidevine 解析器仍可能在输出结束前持有整轨 box 结构。
- `download.codec_alternative`：是否在前一个编码重试耗尽后继续尝试回退链；关闭时只尝试第一个编码。
- `aac-lc` 无需写入 `quality_priority`；开启编码回退时会自动追加为最后的 WebPlayback 保底格式。
- 回退链中的每个编码（含隐式 AAC-LC 保底）均使用 `download.max_attempts`；每个编码的下载阶段和解密阶段分别独立计数重试。
- 重试、耗尽、恢复和编码回退会通过任务 SSE 事件返回；任务详情中的每个项目也会返回 `retry_kind`、`attempt`、`max_attempts` 和 `status_message`，其中 `attempt` 为当前阶段（`retry_kind`）的尝试序号（从 1 开始）。

### 后解密落盘优化实测（当前实现）

优化后的正常链路是“加密输入 → 逐片段解密 → ffmpeg stdin → 扁平化文件 → 完整性校验 → 标签 → 最终保存”。标签阶段对 ffmpeg 常见的尾部 `moov` 布局只重写较小的 `moov` 与封面数据，媒体区保持原位；无法安全原位更新的兼容布局自动退回原来的整轨重写。

ROG（Ryzen 9 9900X、WSL Docker）使用约 70.1 MB、45 个分片、约 90 秒的 192 kHz/24-bit ALAC 离线 fixture 对优化前基线和当前实现做交错 A/B。两者运行在同一生产镜像与 ffmpeg 6.1.2 中；Windows 同盘布局预热后每种模式测 5 次，实际部署布局预热后每种模式测 3 次。下表临时空间为 `download.temp_dir` 与系统临时目录的同时峰值，不含最终下载目录：

| 布局 | 模式 | 优化前 | 当前 | 提速 | 优化前临时空间 | 当前临时空间 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 临时与输出均在 Windows 挂载盘 | Low | 4774.6 ms | 2585.3 ms | 45.9% | 267.3 MiB | 133.7 MiB |
| 临时与输出均在 Windows 挂载盘 | High | 2962.0 ms | 1341.0 ms | 54.7% | 133.6 MiB | 66.8 MiB |
| WSL 本地临时盘 → Windows 输出盘 | Low | 1989.0 ms | 1911.5 ms | 3.9% | 267.3 MiB | 133.7 MiB |
| WSL 本地临时盘 → Windows 输出盘 | High | 1947.0 ms | 1845.9 ms | 5.2% | 133.6 MiB | 66.8 MiB |

全部运行均禁止外网且未产生意外 HTTP 请求，最终文件可完整解码，32-bit PCM SHA-256 与 golden 文件逐次一致。64 MiB 媒体加 1 MiB 封面的标签微基准在同机上从中位数 46.0 ms 降至 11.4 ms（约 4.0 倍）；收益主要来自避免复制整段媒体，不能直接等同于整条下载链路的提速。

当前两种内存模式都已消除明文中间文件，因此 `high` 只比 `low` 少一份加密检查点。在接近实际部署的跨文件系统布局中，本 fixture 的 High 总用时仅比 Low 少约 3.4%，而整轨加密数据会显著提高堆峰值；默认 `low` 因而仍是更稳妥的选择。若临时目录位于较慢的下载盘、内存充足且并发受控，`high` 仍可减少加密检查点 I/O。最终跨文件系统保存仍保留一次 `.part` 复制，以维持完整文件发布与失败清理语义；把 `download.temp_dir` 与下载目录放在同一文件系统可让这一步退化为原子 rename。

### 内存模式历史实测（v1.3.0，优化前）

为排除 Apple CDN 速度波动，测试先缓存两张真实专辑的原始加密 ALAC 媒体，再以只读、禁止回源的本地 HTTP 响应重放；API、元数据解析、`wrapper-manager` 解密、ffmpeg 重封装、完整性解码、标签写入和最终文件保存仍走完整生产链路。每个测试单元使用全新的后端容器、数据库和临时目录，并在 `wrapper-manager` 恢复两个 Ready 实例后开始计时。

- [月姫 -A piece of blue glass moon- THEME SONG E.P](https://music.apple.com/cn/album/1580904295)：8 首，原始媒体约 841 MiB。
- [Fate/stay night [Realta Nua] Soundtrack Reproduction](https://music.apple.com/cn/album/1576634760)：62 首，原始媒体约 1.14 GiB。
- 每个“专辑 × 模式 × 并发”组合交错运行 3 轮；下表为三轮平均值。
- 表中“曲目并发”表示单个专辑任务同时处理的 Enhanced HLS 轨道数；v1.3.0 实际由进程级共享池统一调度，跨任务总量受 `download.max_parallel_downloads`、`download.max_parallel_decrypts` 和内部 in-flight 背压共同约束。
- 内存是整个后端容器的 cgroup 峰值（包含进程内存和计入 cgroup 的文件页缓存）；临时空间是 `download.temp_dir` 与操作系统临时目录的同时峰值，不包含最终下载目录。
- 全部 48 个测试单元成功完成，强制命中 ALAC，缓存回源次数为 0；两种模式、所有并发下的逐轨音频 packet SHA-256 完全一致。

月姫（长曲目、Hi-Res，较容易放大单轨内存和中间文件成本）：

| 曲目并发 | Low 用时 | High 用时 | High 提速 | Low 内存 | High 内存 | Low 临时空间 | High 临时空间 |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | 122.4 s | 96.9 s | 20.8% | 80 MiB | 409 MiB | 471 MiB | 237 MiB |
| 2 | 67.7 s | 52.7 s | 22.2% | 110 MiB | 706 MiB | 851 MiB | 391 MiB |
| 4 | 42.7 s | 33.5 s | 21.6% | 147 MiB | 1,180 MiB | 1,591 MiB | 761 MiB |
| 8 | 33.8 s | 27.0 s | 20.3% | 269 MiB | 2,054 MiB | 3,055 MiB | 1,328 MiB |

Fate（大量短曲目，固定的 session、ffmpeg 启动、校验和标签开销占比更高）：

| 曲目并发 | Low 用时 | High 用时 | High 提速 | Low 内存 | High 内存 | Low 临时空间 | High 临时空间 |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | 300.5 s | 267.4 s | 11.0% | 211 MiB | 365 MiB | 258 MiB | 122 MiB |
| 2 | 172.6 s | 143.4 s | 16.9% | 210 MiB | 363 MiB | 282 MiB | 128 MiB |
| 4 | 102.8 s | 87.0 s | 15.4% | 210 MiB | 479 MiB | 343 MiB | 143 MiB |
| 8 | 76.9 s | 68.8 s | 10.5% | 216 MiB | 604 MiB | 398 MiB | 152 MiB |

在 v1.3.0 的实现中，High 并不是使用了更快的解密算法。Low 每轨需要写入并重读加密 `raw-*`，再写入并由 ffmpeg 重读解密后的 `dec-*`，合计约产生 `4 × 轨道大小` 的额外临时文件流量；High 将一份加密整轨保留在内存，并通过 pipe 让逐片段解密与 ffmpeg 重封装并行进行，因此同时减少磁盘往返、内核/用户态复制和串行等待。当时的实测中，High 还减少了约 6%-12% 的容器 CPU 时间。当前实现的 Low 也已经直接 pipe 到 ffmpeg，不能再用这组历史数据推断 Low 与 High 的现有差距。

当时的选择建议是：内存紧张或曲目大小不可预估时保留默认的 `low`；一般机器使用 `high` 时，应结合 `download.max_parallel_downloads` 与 `download.max_parallel_decrypts` 控制总 in-flight 轨道，以约 4 条同时活跃的高内存轨道作为保守起点，再根据多个任务的实测内存峰值上调。并发 8 在该历史测试中仍有吞吐收益，但已进入收益递减区，而且 High 的内存峰值由“同时活跃的最大几首曲目”决定，不能只按专辑平均曲目大小估算。上述本地重放结果表示排除公网下载后的后端处理能力上限，不代表普通 CDN 环境中的绝对下载时间；当前模式选择请以紧邻本节之前的实测和说明为准。

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

模板变量列表见 `configs/runtime.example.yaml` 注释。目录段中的 `{ArtistName}` 使用集合的归档艺术家（优先专辑艺术家），保证同一专辑的曲目落在同一目录；文件名段使用曲目自身的艺术家。

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

发版时会优先读取仓库内与版本号对应的版本说明：

```text
.github/release-notes/<版本号>.md
```

例如发布 `v1.4.0` 时使用 `.github/release-notes/v1.4.0.md`。该文件不存在或为空时，workflow 才会回退到现有的提交记录自动生成逻辑。自动生成的每条说明会在末尾包含提交作者，以及 commit message 中通过 `Co-authored-by` trailer 声明的协作者，并自动去重。自动生成逻辑位于：

```text
.github/scripts/generate-changelog.js
```

发版成功后 `Release` workflow 会调用 `Docker Publish` workflow，构建多架构镜像（linux/amd64 + linux/arm64）并推送到 `ghcr.io/amdl-web/apple-music-downloader-backend`，镜像 tag 为 `{version}`、`{major}.{minor}` 与 `latest`。`Docker Publish` 也可对已存在的版本标签手动触发（补发或重发镜像），在 GitHub 页面手工发布 Release 时同样会自动运行。手动触发默认不移动 `latest`（避免重发旧版本时把 `latest` 拉回去），仅在勾选 `latest` 选项时才更新；页面手工发布的预发布（prerelease）Release 也不会移动 `latest`。首次推送会在 GHCR 创建私有 package，如需公开拉取，请到仓库 Packages 设置里将其改为 public。
