# API v1

## `GET /api/v1/health`

返回后端和 wrapper-manager 的健康状态。

## `GET /api/v1/capabilities`

返回当前支持的输入类型、音质优先级、外部媒体工具检查结果。

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
  "type": "unknown",
  "status": "queued"
}
```

当前 `force` 字段已预留；第一版会先按目标文件是否存在自动跳过。

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

