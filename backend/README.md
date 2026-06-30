# AMDL Backend

这是新的 AMDL 核心下载后端。当前阶段只关注歌曲下载核心：

- 支持 Apple Music 单曲、专辑、歌单 URL。
- 不做 OAuth、不做通知、不兼容旧前端。
- 直接对接 `wrapper-manager` gRPC。
- 使用 SQLite 持久化任务、任务项和事件。
- 通过 SSE 暴露实时进度。

## 运行

```bash
cd /path/to/apple-music-downloader-backend
go run ./cmd/amdl-api
```

默认配置文件：

```text
/path/to/apple-music-downloader-backend/configs/config.yaml
```

默认监听：

```text
127.0.0.1:18080
```

默认 wrapper-manager：

```text
192.168.3.42:8080
```

## 重试与编码降级

- `download.retries`：元数据、封面和歌词等普通外部调用的重试次数。
- `download.codec`：唯一的首选编码；支持 `alac`、`aac`、`aac-binaural`、`aac-downmix`、`ec3`、`ac3` 和 `aac-lc`。
- `download.retries` 表示普通操作首次尝试之后的额外重试次数；例如 `3` 表示最多尝试 `4` 次。
- 当首选编码不是 `aac-lc` 且歌曲没有 Enhanced HLS 时，固定回退到 WebPlayback AAC-LC。
- 重试、耗尽、恢复和编码回退会通过任务 SSE 事件返回；任务详情中的每个项目也会返回 `retry_kind`、`attempt`、`max_attempts` 和 `status_message`。

## 外部媒体工具

后端自身不再调用旧的编译下载模块，但媒体封装阶段需要这些命令：

- `ffmpeg`
- `gpac`
- `MP4Box`
- `mp4extract`
- `mp4edit`

可以通过接口检查：

```bash
curl http://127.0.0.1:18080/api/v1/capabilities
```

## API

创建下载任务：

```bash
curl -X POST http://127.0.0.1:18080/api/v1/downloads \
  -H 'Content-Type: application/json' \
  -d '{"url":"https://music.apple.com/us/album/example/123456789?i=987654321"}'
```

查询任务：

```bash
curl http://127.0.0.1:18080/api/v1/downloads/{job_id}
```

监听事件：

```bash
curl -N http://127.0.0.1:18080/api/v1/downloads/{job_id}/events
```

取消任务：

```bash
curl -X POST http://127.0.0.1:18080/api/v1/downloads/{job_id}/cancel
```

## 保存格式

默认按“艺术家 / 专辑 / 歌曲”保存：

```text
data/downloads/{ArtistName}/{AlbumName}/{TrackNumber:02d}. {SongName}.m4a
```

歌单下载单独保存到以歌单名命名的文件夹，歌曲文件直接放在该文件夹内：

```text
data/downloads/{PlaylistName}/{SongNumer:02d}. {SongName}.m4a
```
