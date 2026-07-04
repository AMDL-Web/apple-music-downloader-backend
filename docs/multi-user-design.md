# 多用户方案设计文档

状态:设计稿(未实施) · 日期:2026-07-03

## 1. 目标与范围

为根后端(Go, `net/http` + SQLite)引入多用户能力,效果对齐参考A(`explore/amdl-myversion`),并在其基础上新增**管理员 / 普通用户**两级角色:

- 认证沿用参考A模式:后端不做登录,信任 Nginx + OAuth2-Proxy 注入的 `X-User` / `X-Email` 头(Synology OIDC 或 Apple Sign In)。
- 用户仅由管理员创建,无自助注册。
- 普通用户:提交/查看/取消**自己的**任务、查询音质。管理员:全部任务、wrapper 账号登录登出、用户管理、系统配置。
- 每用户独立下载目录,任务归属落库。

非目标:密码体系、自助注册、用户级配额/优先级调度(预留扩展点)。

## 2. 与参考A的差异对照

| 方面 | 参考A | 本方案 |
|---|---|---|
| 用户存储 | `users.yaml` 文件 + FileLock | SQLite `users` 表(与现有 jobs 同库,事务一致) |
| 认证 | Nginx auth_request → OAuth2-Proxy → X-User/X-Email | 相同 |
| 角色 | 无,扁平权限 | `role: admin \| user` |
| 用户名映射 | `normalize_username`(标准名/别名/邮箱,忽略大小写) | 相同语义,查表实现 |
| 任务归属 | task_queue.json 中 `user` 字段 | `jobs.user_id` 外键 |
| 目录隔离 | source.yaml `{user}` 占位符替换 | `downloads_dir/{user}/...` 路径注入 |
| 用户级配置覆盖 | `source_overrides` | `users.overrides_json`(阶段二,可选) |

## 3. 总体架构

```
浏览器
  │ Cookie: amdl_oauth2_proxy
  ▼
Nginx ── auth_request /oauth2/auth ──► OAuth2-Proxy(Synology OIDC / Apple)
  │  认证通过后注入:
  │    X-User / X-Email / X-Internal-Secret(共享密钥,见 §8)
  ▼
Go 后端(127.0.0.1:18080,仅本机监听)
  identity 中间件 → 解析身份 → 注入 context → 各 handler 做角色/归属检查
```

信任边界:后端只接受来自 Nginx 的请求。`X-User`/`X-Email` 属可伪造头,必须满足 §8 的防护约束才可信。

## 4. 数据模型(SQLite)

新增两张表,`jobs` 加归属列。按 AGENTS.md 允许破坏性变更,直接在 `internal/db/db.go` 的建表语句中扩展,并提供一次性迁移(§9)。

```sql
CREATE TABLE IF NOT EXISTS users (
    id            TEXT PRIMARY KEY,            -- uuid
    username      TEXT NOT NULL UNIQUE COLLATE NOCASE,  -- 标准用户名,如 lyjw
    role          TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('admin','user')),
    avatar_url    TEXT NOT NULL DEFAULT '',
    enabled       INTEGER NOT NULL DEFAULT 1,  -- 禁用即拒绝访问(软删除)
    overrides_json TEXT NOT NULL DEFAULT '{}', -- 用户级配置覆盖(阶段二)
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);

-- 别名与邮箱统一为"身份标识"表,对应参考A的 other_name + email 两类匹配
CREATE TABLE IF NOT EXISTS user_identities (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id  TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind     TEXT NOT NULL CHECK (kind IN ('alias','email')),
    value    TEXT NOT NULL COLLATE NOCASE,
    UNIQUE(kind, value)
);

ALTER TABLE jobs ADD COLUMN user_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_jobs_user_id ON jobs(user_id);
```

身份解析顺序(等价参考A `normalize_username`,utils.py:272):先按 `X-User` 匹配 `users.username` 与 `user_identities(kind='alias')`,未命中再按 `X-Email` 匹配 `kind='email'`,均忽略大小写。未命中 → 403(用户未注册,请联系管理员)。

## 5. identity 中间件与 domain 扩展

新增 `internal/auth` 包:

```go
type Identity struct {
    UserID   string
    Username string
    Role     domain.Role   // "admin" | "user"
}

// Middleware:
// 1. 校验 X-Internal-Secret(若配置)
// 2. 读取 X-User / X-Email → 查库解析 → 未命中或 disabled 则 403
// 3. context.WithValue 注入 Identity
// 4. 白名单路径跳过:GET /api/v1/health、/docs、/api/openapi.yaml
func Middleware(store *db.Store, cfg config.AuthConfig) func(http.Handler) http.Handler

func RequireAdmin(next http.HandlerFunc) http.HandlerFunc  // 非 admin → 403
func FromContext(ctx context.Context) (Identity, bool)
```

在 `internal/api/server.go` 的 `Routes()` 中挂载:`cors(auth.Middleware(...)(mux))`。

## 6. API 权限矩阵

现有路由(internal/api/server.go:51-62)与新增路由:

| 路由 | 普通用户 | 管理员 |
|---|---|---|
| GET /api/v1/health, /docs, openapi.yaml | 免认证 | 免认证 |
| GET /api/v1/capabilities | ✅ | ✅ |
| POST /api/v1/quality | ✅ | ✅ |
| POST /api/v1/downloads | ✅(归属自己) | ✅ |
| GET /api/v1/downloads | ✅ 仅返回自己的 | ✅ 全部;`?user=` 可过滤 |
| GET /api/v1/downloads/{id}(含 /events SSE) | ✅ 仅自己的,否则 404 | ✅ 任意 |
| POST /api/v1/downloads/{id}/cancel | ✅ 仅自己的 | ✅ 任意 |
| GET /api/v1/wrapper/status | ❌ 403 | ✅ |
| POST /api/v1/wrapper/login · /2fa · /logout | ❌ 403 | ✅ |
| **GET /api/v1/me**(新增) | ✅ 返回自己 username/role/avatar | ✅ |
| **GET/POST /api/v1/users**(新增) | ❌ | ✅ 列表 / 创建 |
| **GET/PATCH/DELETE /api/v1/users/{id}**(新增) | ❌ | ✅ 查看 / 改角色·别名·邮箱·enabled·avatar / 禁用 |

要点:非归属任务一律返回 **404**(而非 403),避免泄露任务存在性。`GET /api/v1/me` 供前端渲染头像与角色菜单(对应参考A的 `/api/user/avatar`,userProfile.js)。SSE 端点走同域 Cookie,天然通过 auth_request,无需像参考A那样对 `/api/progress/` 豁免;Nginx 侧需为其关闭 `proxy_buffering`。

## 7. 下载目录与资源隔离

现配置 `download.downloads_dir: "data/downloads"`,类型子目录 `songs/albums/playlists/artists`。改为按用户插入一层:

```
data/downloads/{username}/albums/{ArtistName}/{AlbumName}/...
```

实现:任务执行时从 job 的 `user_id` 取 username,`media.Downloader` 将根目录解析为 `filepath.Join(cfg.Download.DownloadsDir, username)`,其余路径模板逻辑不变。username 已由建库约束保证可作目录名(见 §6 用户创建校验:`^[a-z0-9_-]{1,32}$`)。临时目录 `tmp` 保持全局。生产部署可像参考A(docker-compose.yml)那样把各用户目录挂到不同宿主卷。

任务队列保持**全局共享、FIFO**(与参考A一致);`jobs` 表已有 `user_id`,后续如需按用户并发限额,在 Manager 出队处按 user 计数即可(预留,不在本期)。

## 8. 信任头安全约束(关键)

后端信任 `X-User` 的前提,三层防护(前两层为部署要求,第三层代码实现):

1. `server.listen` 保持 `127.0.0.1:18080`(现配置已是),仅 Nginx 可达;容器部署时不对外暴露端口。
2. Nginx 对上游请求**无条件覆盖** `X-User`/`X-Email`/`X-Internal-Secret`(`proxy_set_header` 天然覆盖客户端同名头),参考A nginx.conf:117-159 已是此写法。
3. 后端新增配置 `auth.internal_secret`:非空时,请求头 `X-Internal-Secret` 不匹配则 401。防止同机其他进程直连伪造。

## 9. 配置与迁移

`configs/config.yaml` 新增(按 AGENTS.md 要求补全注释):

```yaml
auth:
  # bool: true 启用多用户(信任头模式);false 退化为单用户,所有请求视为内置 admin。默认 false。
  enabled: true
  # string: 与 Nginx 约定的共享密钥;空字符串表示不校验 X-Internal-Secret。
  internal_secret: ""
  # string: 引导管理员用户名。首次启动且 users 表为空时自动创建该 admin(解决"第一个管理员由谁创建"),之后忽略。
  bootstrap_admin: "lyjw"
  # string: 引导管理员邮箱,写入 user_identities(kind=email),可为空。
  bootstrap_admin_email: ""
```

数据迁移:启动时若检测到 `jobs.user_id` 列缺失则 ALTER 添加;存量任务统一归到 bootstrap admin。存量下载文件按新目录规则**不搬迁**(仅新任务生效),避免破坏 Emby 等外部索引。

## 10. 实施阶段

阶段一(核心):users/user_identities 表 + jobs.user_id;auth 中间件与 bootstrap;downloads 归属过滤与 404 语义;wrapper 路由加 RequireAdmin;/me 与 /users 管理 API;按用户下载目录;OpenAPI 与测试更新(server_test.go 的路由权限矩阵需同步)。

阶段二(对齐参考A完整体验,可选):`overrides_json` 用户级配置覆盖(路径模板、歌词开关等白名单键);用户级通知配置(Bark/Emby/邮件,参考A users.yaml 字段);Web UI(登录态由 OAuth2-Proxy 处理,前端仅调 /me + 任务 API;管理页仅 admin 可见)。

阶段三(运维):Nginx + OAuth2-Proxy 部署清单(可直接复用参考A `oauth2-proxy/` 两套配置与 nginx.conf 的 auth_request 段,上游改指 `127.0.0.1:18080`)。

## 11. 风险与决策记录

- **X-User 伪造**:以 §8 三层防护缓解;`internal_secret` 建议生产必配。
- **OAuth2-Proxy 单点**:认证全部外包,代理故障则全站不可用,与参考A一致,接受。
- **用户删除语义**:默认软禁用(`enabled=0`)保留任务归属;硬删除 CASCADE 只删身份映射,jobs 保留 user_id 供审计。
- **`auth.enabled=false` 退化模式**:保证单用户场景与现状为兼容,也便于本地开发和现有 CI 通过。
