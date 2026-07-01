# API v1

## `GET /api/v1/health`

轻量后端存活检查，仅验证 API 进程与本地数据库，**不**探测 wrapper-manager。适合负载均衡或进程监控。

成功时返回 `200 OK`：

```json
{"status": "ok"}
```

数据库不可用时返回 `503 Service Unavailable`：

```json
{"status": "degraded", "database_error": "..."}
```

## `GET /api/v1/capabilities`

返回当前支持的输入类型、音质优先级、外部媒体工具检查结果。

## Wrapper 管理

### `GET /api/v1/wrapper/status`

探测 wrapper-manager 连通性与运行状态（登录、区域、客户端数等）。下载或账号管理前应使用此接口，而非 `/api/v1/health`。

返回 wrapper-manager 状态：

```json
{
  "ready": true,
  "status": true,
  "regions": ["us"],
  "client_count": 1
}
```

wrapper-manager 不可用时返回 `503 Service Unavailable`。

### `POST /api/v1/wrapper/login`

开始登录：

```json
{
  "username": "apple-id@example.com",
  "password": "password"
}
```

无需两步验证时返回 `200 OK`：

```json
{"status":"logged_in"}
```

需要两步验证时返回 `202 Accepted`：

```json
{"status":"needs_2fa","login_id":"opaque-login-id"}
```

### `POST /api/v1/wrapper/login/{login_id}/2fa`

继续同一次登录并提交验证码：

```json
{"two_step_code":"123456"}
```

成功时返回 `200 OK` 和 `{"status":"logged_in"}`。`login_id` 仅保存在内存中；验证码等待与验证完成共用 `wrapper.login_timeout_seconds` 时限（默认 120 秒），服务重启后失效。

### `POST /api/v1/wrapper/logout`

```json
{"username":"apple-id@example.com"}
```

成功时返回：

```json
{"status":"logged_out","username":"apple-id@example.com"}
```

认证接口可能返回 `400`（参数错误）、`401`（认证失败）、`404`（会话或账号不存在）、`409`（重复操作）、`502`（wrapper 上游错误）或 `504`（登录超时）。

## `POST /api/v1/downloads`

创建下载任务。

请求：

```json
{
  "url": "https://music.apple.com/us/album/name/123?i=456",
  "force": false
}
```

响应：`202 Accepted`

```json
{
	"id": "job_xxx",
	"input": "...",
	"type": "song",
	"storefront": "us",
	"force": false,
	"status": "queued"
}
```

`force: true` 会覆盖已存在的音频和歌词边车文件；默认为 `false`，已存在的文件会被跳过。链接类型和区域在入队前完成校验。

歌词行为由后端配置控制：

- `download.embed_lyrics`: 写入 MP4 歌词标签。
- `download.save_lyrics_file`: 保存 `.lrc` 或 `.ttml` 边车文件。
- `download.lyrics_format`: `lrc` 或 `ttml`。
- `download.lyrics_type`: `lyrics` 或 `syllable-lyrics`。
- `download.lyrics_extras`: 可选 `translation`、`pronunciation`。

歌词请求通过 wrapper-manager 完成，需要有效 Apple Music 订阅登录态。歌词获取或转换失败不会使下载任务失败，任务项会继续下载并记录无歌词原因。

## `GET /api/v1/downloads`

列出任务。

查询参数：

- `limit`: 默认 50，最大 200。

## `GET /api/v1/downloads/{job_id}`

返回任务和任务项。

## `POST /api/v1/downloads/{job_id}/cancel`

取消任务。

## `GET /api/v1/downloads/{job_id}/events`

SSE 事件流。支持 `Last-Event-ID` 断点续接。

事件类型包括：

- `job_queued`
- `job_started`
- `resolved_input`
- `item_progress`
- `codec_selected`
- `codec_failed`
- `item_skipped`
- `item_completed`
- `item_failed`
- `job_finished`
- `job_failed`
- `job_cancelled`
