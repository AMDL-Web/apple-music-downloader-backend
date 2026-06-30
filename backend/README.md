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
- `download.quality_priority`：按顺序尝试的 Enhanced HLS 编码回退链；支持 `alac`、`aac`、`aac-binaural`、`aac-downmix`、`ec3` 和 `ac3`。
- `download.codec_alternative`：是否在前一个编码重试耗尽后继续尝试回退链；关闭时只尝试第一个。
- `aac-lc` 无需写入 `quality_priority`；开启回退时会自动追加为最后的 WebPlayback 保底格式。
- `download.retries` 表示普通操作首次尝试之后的额外重试次数；例如 `3` 表示最多尝试 `4` 次。
- 只有回退链的第一个编码使用 `download.retries`；后续编码（包括隐式 AAC-LC 保底）均只尝试一次。
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
  -d '{"url":"https://music.apple.com/us/album/example/123456789?i=987654321","force":true}'
```

`force: true` 会覆盖已存在的音频及其歌词边车文件；默认为 `false`，已存在的文件会被跳过。

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

不同下载任务会保存到 `downloads` 下独立的类型目录。单曲默认按“艺术家 / 专辑 / 歌曲”保存：

```text
data/downloads/songs/{ArtistName}/{AlbumName}/{TrackNumber:02d}. {SongName}.m4a
```

专辑任务保存到：

```text
data/downloads/albums/{ArtistName}/{AlbumName}/{TrackNumber:02d}. {SongName}.m4a
```

歌单下载单独保存到以歌单名命名的文件夹，歌曲文件直接放在该文件夹内：

```text
data/downloads/playlists/{PlaylistName}/{SongNumer:02d}. {SongName}.m4a
```

三个类型目录名可分别通过 `download.songs_folder_name`、`download.albums_folder_name`、`download.playlists_folder_name` 自定义。

可选择在音频文件外额外保存独立封面：

- `download.save_album_cover: true`：在专辑目录保存 `cover.jpg`/`cover.png`。
- `download.save_artist_cover: true`：在艺术家目录保存 `artist.jpg`/`artist.png`。
- `download.save_playlist_cover: true`：在歌单目录保存 `cover.jpg`/`cover.png`。

文件扩展名跟随 `download.cover_format`。歌单为平铺目录，可保存歌单封面，但不会额外写入专辑或艺术家封面。

对于带 `?i=<song_id>` 的专辑链接，可通过 `catalog.album_track_url_mode` 选择任务类型：

- `song`（默认）：视为单曲任务，使用 `i` 参数中的歌曲 ID。
- `album`：忽略 `i` 参数，视为专辑任务并下载整张专辑。
