# AMDL Backend

AMDL Backend 是 Apple Music 下载系统的核心后端服务。它负责解析 Apple Music 单曲、专辑、歌单和艺术家链接，调度下载任务，对接 `wrapper-manager` 获取媒体数据，并通过 HTTP API 与 SSE 暴露任务状态。

当前仓库以根目录 Go 模块为生产代码来源，主要代码位于 `cmd/`、`internal/`、`configs/` 等目录。

## 功能

- 支持 Apple Music 单曲、专辑、歌单和艺术家 URL。
- 不会支持 Apple Music MV 下载；受 L3 限制，当前链路只能获取低分辨率视频，不符合本项目的下载质量目标。
- 通过 `wrapper-manager` gRPC 获取账号状态、播放清单和媒体数据。
- 使用 SQLite 持久化任务、任务项和事件。
- 通过 SSE 推送下载进度。
- 支持 Enhanced HLS 编码回退链和 AAC-LC 保底格式。
- 支持歌词嵌入、歌词边车文件、封面嵌入和独立封面保存。
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

启动前请按实际环境修改 `configs/config.yaml`，尤其是：

- `server.listen`：API 监听地址。
- `wrapper.address`：`wrapper-manager` gRPC 地址。
- `database.path`：SQLite 数据库路径。
- `download.downloads_dir`：下载文件保存目录。
- `tools.*`：外部媒体工具命令路径或命令名。

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

检查外部能力：

```bash
curl http://localhost:18080/api/v1/capabilities
```

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

查询任务：

```bash
curl http://localhost:18080/api/v1/downloads/{job_id}
```

列出任务（支持分页与筛选）：

```bash
curl 'http://localhost:18080/api/v1/downloads?limit=20&offset=0&status=failed,cancelled&type=album&storefront=cn&q=beta&created_after=2024-07-01&sort=updated_at&order=desc'
```

可用查询参数：`limit`、`offset`、`status`、`type`、`storefront`、`q`、`created_after`、`created_before`、`updated_after`、`updated_before`、`sort`、`order`。响应额外返回 `total`、`limit`、`offset`。

监听列表变化（SSE，可带与列表相同的内容筛选参数）：

```bash
curl -N 'http://localhost:18080/api/v1/downloads/events?status=running,queued&type=album'
```

WebSocket 版本为 `/api/v1/downloads/events/ws`，筛选参数相同，续接用 `last_event_id`。

监听任务事件：

```bash
curl -N http://localhost:18080/api/v1/downloads/{job_id}/events
```

取消任务：

```bash
curl -X POST http://localhost:18080/api/v1/downloads/{job_id}/cancel
```

## 下载行为

仅音频下载是受支持目标。Apple Music MV 下载不会支持，因为 L3 限制下只能获取低分辨率视频。

### 重试与编码降级

- `download.max_attempts`：元数据、封面、歌词以及每个编码的下载/解密阶段的最大总尝试次数（含首次）。例如 `4` 表示每个操作最多尝试 4 次；值 `<= 0` 按 1 处理（仅尝试一次，不重试）。
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

不同下载任务会保存到 `download.downloads_dir` 下独立的类型目录。

单曲默认保存到：

```text
data/downloads/songs/{ArtistName}/{AlbumName}/{TrackNumber:02d}. {SongName}.m4a
```

专辑默认保存到：

```text
data/downloads/albums/{ArtistName}/{AlbumName}/{TrackNumber:02d}. {SongName}.m4a
```

艺术家任务会展开为该艺术家的专辑/单曲列表，并保存到 artists 类型目录：

```text
data/downloads/artists/{ArtistName}/{AlbumName}/{TrackNumber:02d}. {SongName}.m4a
```

歌单默认保存到：

```text
data/downloads/playlists/{PlaylistName}/{SongNumer:02d}. {SongName}.m4a
```

四个类型目录名可分别通过 `download.songs_folder_name`、`download.albums_folder_name`、`download.playlists_folder_name`、`download.artists_folder_name` 自定义。

可选择在音频文件外额外保存独立封面：

- `download.save_album_cover: true`：在专辑目录保存 `cover.jpg` 或 `cover.png`。
- `download.save_artist_cover: true`：在艺术家目录保存 `artist.jpg` 或 `artist.png`。
- `download.save_playlist_cover: true`：在歌单目录保存 `cover.jpg` 或 `cover.png`。

文件扩展名跟随 `download.cover_format`。歌单为平铺目录，可保存歌单封面，但不会额外写入专辑或艺术家封面。

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

推送 `v*` tag 或手动运行 `Release` workflow 时，GitHub Actions 会先执行完整 Go 测试，再创建 GitHub Release。

Release changelog 由 GitHub generated release notes 自动生成，分类规则位于：

```text
.github/release.yml
```
