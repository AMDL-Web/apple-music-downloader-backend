# 多链接提交与去重设计方案

参考 Reference A（`explore/amdl-myversion`）的批量提交 + 三层去重模型，适配本仓库 Go 后端架构。

## 1. 现状与差距

| 能力 | 本仓库现状 | Reference A |
|---|---|---|
| 提交入口 | `POST /api/v1/downloads`，单个 `{url, force}` | `POST /api/task`，JSON 数组批量提交 |
| 输入切分 | 无 | 前端按 `\n , ; 空格 ，；` 切分 textarea |
| 请求内去重 | 无 | set 去重（`请求内重复`） |
| 队列去重 | 无（同一 URL 可重复入队） | 扫描队列按 `(user, link)` 去重 |
| 归一化 | `ParseWithAlbumTrackMode` 已解析 type/storefront/id，但未持久化 id | 剥离 `?i=`、正则解析后按归一化链接比对 |
| 批量响应 | 无 | `accepted_count / failed_count / failure_summary` |

关键缺口：jobs 表无解析后的规范键（canonical key），无任何唯一约束，去重只能靠下载阶段的文件存在检查。

## 2. 设计目标

- 一次请求提交多个链接，逐条返回结果（接受 / 无效 / 重复 / 队列满）。
- 三层去重：请求内 → 活跃任务 → DB 唯一索引兜底。
- 去重键与 URL 写法无关（`?i=`、beta/classical 域名、大小写 storefront 均归一）。
- **不保留旧接口形态**（依 AGENTS.md：正确性与架构优先于向后兼容）。单链接即长度为 1 的批量，全系统只有一条提交路径、一种响应结构。

## 3. 去重键（canonical key)

格式：`<type>:<storefront>:<id>`，如 `album:us:1713845538`。

- 由 `applemusic.ParseWithAlbumTrackMode` 的结果构造，天然吸收 `?i=` 与 `album_track_url_mode` 语义（同一 URL 在 song/album 模式下键不同，符合预期）。
- 含 storefront：不同区同专辑视为不同任务（输出路径/曲目可能不同）。
- **只对活跃任务（queued/running）去重**。completed/failed/cancelled 允许重新提交——重复下载由现有 item 级 `skipped_existing` 文件检查兜底，且 `force` 语义保持不变（force 只控制覆盖文件，不绕过活跃去重）。

Reference A 的第三层去重（song→album 元数据阶段转换后再查重）在本仓库不需要单独实现：`album_track_url_mode=album` 已在解析期完成同等归一。

### 3.1 album_track_url_mode 对去重的影响

模式只改变 `/album/xxx?i=SONG_ID` 的解析（`url.go`），去重行为随之不同：

| 输入 | mode=album | mode=song |
|---|---|---|
| `album/111` | `album:us:111` | `album:us:111` |
| `album/111?i=222` | `album:us:111`（与上互相去重） | `song:us:222`（不与上去重） |
| `album/111?i=333` | `album:us:111`（同上） | `song:us:333`（独立任务） |
| `/song/x/222` 直链 | `song:us:222` | `song:us:222`（与 `?i=222` 写法互相去重） |

两种模式下去重结果都与实际下载内容一致，无需特殊处理。已知边界：

- **包含关系不去重**：活跃 `album:us:111` 与其单曲 `song:us:222` 键不同，均会被接受。本仓库 song 任务不做 song→album 升级（`resolveCollection` 只取单曲），输出路径也不同（`single tracks/`），属预期语义而非缺陷。
- **配置切换**：key 按提交时的模式计算并持久化，切换 `album_track_url_mode` 后同一原始 URL 产生不同 key，不与切换前的活跃任务去重。可接受，不做迁移。

## 4. 改动点

### 4.1 domain（`internal/domain/domain.go`）

```go
// 破坏性变更：移除单数 URL 字段，批量是唯一形态
type DownloadRequest struct {
    URLs  []string `json:"urls"`
    Force bool     `json:"force"`
}

type SubmitResult struct {
    URL           string `json:"url"`
    Status        string `json:"status"` // accepted | invalid | duplicate_in_request | duplicate_active | queue_full
    Job           *Job   `json:"job,omitempty"`
    ExistingJobID string `json:"existing_job_id,omitempty"` // duplicate_active 时返回
    Error         string `json:"error,omitempty"`           // invalid 时返回 code+message
}

type BatchSubmitResponse struct {
    Accepted int            `json:"accepted"`
    Rejected int            `json:"rejected"`
    Results  []SubmitResult `json:"results"`
}
```

`Job` 增加 `CanonicalKey string`。

### 4.2 DB（`internal/db/db.go`）

直接修改 `initSchema` 的建表语句（破坏性迁移，不做 ALTER 探测）：

```sql
CREATE TABLE IF NOT EXISTS jobs (
    ...
    canonical_key TEXT NOT NULL,   -- 新增列，加入建表语句
    ...
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_jobs_active_key
    ON jobs(canonical_key)
    WHERE status IN ('queued','running');
```

- 部分唯一索引是最终兜底：即使应用层竞态漏判，INSERT 也会失败，映射为 `duplicate_active`。
- **迁移影响（需确认后执行）**：旧库 jobs 表缺 `canonical_key` 列，与新 schema 不兼容。方案为删除旧 `data/` 下的 SQLite 库重建（历史任务记录丢失，已下载文件不受影响）。AGENTS.md 允许破坏性 schema 变更，但按其要求在实施前说明此影响。
- 新增查询：`FindActiveJobByKey(ctx, key) (Job, bool, error)`。

### 4.3 校验（`internal/jobs` + `internal/media/downloader.go`）

`ValidationResult` 增加 `ID string`，`ValidateRequest` 透传 `parsed.ID`，由 manager 拼 canonical key。

### 4.4 Manager（`internal/jobs/manager.go`）

新增 `SubmitBatch(ctx, urls []string, force bool) BatchSubmitResponse`，流程：

1. 逐条 `ValidateRequest` → 失败标 `invalid`（复用现有 `RequestError` 的 code/message）。
2. 请求内去重：`map[string]bool`（canonical key），后出现者标 `duplicate_in_request`。
3. 持锁 `submitMu` 逐条处理通过项：
   - `FindActiveJobByKey` 命中 → `duplicate_active` + existing_job_id；
   - 队列已满 → `queue_full`（剩余全部标记，已接受的不回滚）;
   - 否则 CreateJob（含 canonical_key）+ 事件 + 入队 → `accepted`；INSERT 撞唯一索引按 `duplicate_active` 处理。

现有 `Submit(ctx, DownloadRequest)` 直接删除，`SubmitBatch` 是唯一提交入口（内部调用方如恢复逻辑一并迁移），不存在双路径。

### 4.5 API（`internal/api/server.go`）

`createDownload`（破坏性变更，旧的单 `url` 请求体不再接受）：

- 服务端对 `urls` 每个条目按 `[\r\n\s,;，；]+` 切分（容忍客户端整段粘贴）、trim、去掉空项。空列表 → 400。
- 统一返回 `BatchSubmitResponse`：有任一 accepted 用 `202`，全部被拒用 `422`。单链接同样走此结构（`results` 长度为 1），客户端只需处理一种响应。
- 上限保护：单次最多 100 条，超出返回 400。
- 同步更新：`server_test.go` 的接口断言、CLI/前端等所有调用方。

### 4.6 配置（`configs/config.yaml`）

不新增配置项。去重始终开启（`force` 不绕过），无需开关；如未来需要按 completed 去重再加 `dedup_scope`。

## 5. 响应示例

```json
{
  "accepted": 2,
  "rejected": 2,
  "results": [
    {"url": "https://music.apple.com/us/album/x/111", "status": "accepted", "job": {"id": "job_ab12", "...": "..."}},
    {"url": "https://music.apple.com/us/album/x/111?i=222", "status": "duplicate_in_request"},
    {"url": "https://music.apple.com/us/album/y/333", "status": "duplicate_active", "existing_job_id": "job_cd34"},
    {"url": "https://example.com/z", "status": "invalid", "error": "invalid_url: unsupported host \"example.com\""}
  ]
}
```

## 6. 测试要点

- 同批重复（含 `?i=` 变体、beta 域名变体）→ 仅首条 accepted。
- 已有 queued/running 同键任务 → `duplicate_active`；completed 同键 → 可再提交。
- 并发批量提交同键 → 唯一索引兜底，恰好一条 accepted。
- 队列容量边界：部分 accepted + 其余 `queue_full`。
- 单元素批量（原单链接场景）→ `results` 长度为 1，语义与批量一致。
- 旧格式请求体 `{"url": "..."}` → 400（urls 为空）。

## 7. 实施顺序

1. db：schema 加列 + 部分唯一索引 + `FindActiveJobByKey`（旧库删除重建，实施前确认）。
2. ValidationResult 加 ID，downloader 透传。
3. manager：`SubmitBatch` 替换并删除 `Submit`。
4. api：新请求体 / 统一批量响应，更新所有调用方与测试断言。
5. 测试（manager 并发 + api 集成）。
