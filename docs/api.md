# HTTP API

面板 API 前缀为 `{base_path}/api/v1`，默认 `base_path` 为 `/`。除登录、健康检查与订阅外，全部接口需要会话 Cookie。

部署在子路径（如 `base_path: /panel`）时，API 前缀为 `/panel/api/v1`；访问 `/panel` 会 308 重定向到 `/panel/`。

## 约定

- 请求/响应均为 JSON，字符集 UTF-8。
- 成功返回资源对象或列表；列表形如：

```json
{ "items": [], "total": 0 }
```

- 错误统一为：

```json
{ "error": { "code": "invalid_request", "message": "...", "fields": {} } }
```

- `message` 面向调用方：校验错误、端口占用、权限不足会说明原因；`500 internal_error` 只返回通用文案，`409 conflict` 只说明冲突字段（例如重名用户），内部细节（SQLite 约束名、文件路径）只写日志。

- 两步验证相关错误码：`invalid_totp`（验证码或恢复码错误）、`totp_challenge_expired`（挑战过期，需重新登录）、`totp_challenge_invalid`（挑战无效，例如两步之间密码被修改）、`totp_already_enabled`、`totp_not_setup`、`totp_not_enabled`、`invalid_password`。`totp_required` 是登录响应字段而不是错误码。

- 时间字段为 UTC Unix 秒（`expires_at`、`created_at` 等）；前端按本地时区展示。
- 会话 Cookie 名 `sspanel_session`，`HttpOnly`、`SameSite=Strict`，HTTPS 时带 `Secure`。
- 变更请求若带 `Origin` 头，必须与 `Host` 同源，否则返回 `403 invalid_origin`。
- 登录失败限制：默认 5 次/分钟/IP，超出返回 `429 too_many_attempts`。真实客户端 IP 只在请求来自 `panel.trusted_proxies` 时采用 `X-Forwarded-For`。
- `/api/v1` 的 JSON 请求体上限为 1 MiB，超出返回 `413 payload_too_large`。
- 面板静态资源在请求带 `Accept-Encoding: gzip` 时返回 `Content-Encoding: gzip`，并始终带 `Vary: Accept-Encoding`；`HEAD` 与 `Range` 请求返回未压缩文件。API 的 JSON 响应默认不压缩，只有两个不含凭据的用户列表端点是例外（见下一条）。
- `/api/v1` 的全部响应带 `Cache-Control: no-store` 与 `Pragma: no-cache`：响应可能包含用户 PSK、订阅 token 等信息，不允许任何缓存保存副本。
- 只有 `GET /clients` 与 `GET /inbounds/:id/clients` 两个列表端点在响应体 ≥ 4 KiB 且请求带 `Accept-Encoding: gzip` 时返回 `Content-Encoding: gzip`（这两个响应不含凭据，压缩没有 BREACH 类风险）。其余 API 响应不压缩；所有可能被压缩的响应都带 `Vary: Accept-Encoding`。

## 认证

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/auth/login` | body `{username, password}`；未启用两步验证时成功即 Set-Cookie，启用时返回挑战 |
| POST | `/auth/login/totp` | body `{challenge, code}`，完成两步登录并 Set-Cookie |
| POST | `/auth/logout` | 清除会话 |
| GET | `/auth/me` | 当前管理员信息（含 `totp_enabled`） |
| PUT | `/auth/password` | body `{current_password, new_password}`，修改后轮换会话 |
| POST | `/auth/totp/setup` | body `{password}`，生成待绑定的 TOTP 密钥 |
| POST | `/auth/totp/enable` | body `{code}`，完成绑定并返回恢复码 |
| POST | `/auth/totp/disable` | body `{password, code}`，关闭两步验证 |
| POST | `/auth/totp/recovery-codes` | body `{password, code}`，重生成恢复码 |

登录响应（未启用两步验证）：

```json
{
  "admin": {
    "id": 1,
    "username": "admin",
    "must_change_password": false,
    "totp_enabled": false
  },
  "expires_at": 1790000000
}
```

登录响应（已启用两步验证）：

```json
{ "totp_required": true, "challenge": "…", "expires_at": 1790000000 }
```

- 启用两步验证后，密码校验通过只返回挑战，**不下发会话 Cookie**；`challenge` 是 5 分钟有效的 HMAC 签名令牌，绑定管理员与会话版本（改密码或两步验证状态变化后失效）。
- 第二步调用 `POST /auth/login/totp`，`code` 可以是 6 位 TOTP 验证码或一次性恢复码。成功后返回 `200`，响应形状与普通登录相同并下发 Cookie。
- 第二步错误码：`400 invalid_request`（请求体不合法）、`401 invalid_totp`（验证码或恢复码错误）、`401 totp_challenge_expired`（挑战过期，需重新登录）、`401 totp_challenge_invalid`（密码已修改或两步验证状态变化）、`429 too_many_attempts`。
- **会话随两步验证状态失效**：启用或关闭两步验证都会改变会话版本，之前签发的会话与登录挑战立即失效（进行该操作的那个会话会被重发 cookie，不会掉线）。
- 密码错误返回 `401 invalid_credentials`；超过限流返回 `429 too_many_attempts`。第二步与密码步共用同一套按 IP 限流（默认 5 次/分钟）：密码步成功**不会**清空失败计数，只有完整登录成功（拿到会话）才会清空——否则知道密码的人可以靠反复走密码步重置预算，把 6 位验证码变成可穷举。

`GET /auth/me` 响应：

```json
{
  "id": 1,
  "username": "admin",
  "must_change_password": false,
  "totp_enabled": true,
  "created_at": 1780000000,
  "last_login_at": 1790000000
}
```

`must_change_password=true` 时，除 `/auth/me`、`/auth/password`、`/auth/logout` 外的接口返回
`403 password_change_required`；`/auth/totp/*` 也在此列，需先改密码。

### 两步验证管理

| 方法 | 路径 | 请求体 | 成功响应 |
|---|---|---|---|
| POST | `/auth/totp/setup` | `{password}` | `{secret, otpauth_url}` |
| POST | `/auth/totp/enable` | `{code}` | `{recovery_codes: [...], totp_enabled: true}` |
| POST | `/auth/totp/disable` | `{password, code}` | `{ok: true, totp_enabled: false}` |
| POST | `/auth/totp/recovery-codes` | `{password, code}` | `{recovery_codes: [...]}` |

| 路径 | 错误 |
|---|---|
| `/auth/totp/setup` | `400 invalid_password`、`409 totp_already_enabled` |
| `/auth/totp/enable` | `400 totp_not_setup`、`400 invalid_totp`、`409 totp_already_enabled` |
| `/auth/totp/disable` | `400 invalid_password`、`400 invalid_totp`、`400 totp_not_enabled` |
| `/auth/totp/recovery-codes` | `400 invalid_password`、`400 invalid_totp`、`400 totp_not_enabled` |

- `setup` 返回 20 字节随机密钥（base32）与 `otpauth://` URI；待绑定密钥在完成绑定前不参与登录校验，因此绑定流程中断不影响当前登录。返回 `409 totp_already_enabled` 时需先关闭再重新绑定。
- `enable` 用待绑定密钥对应的 6 位验证码确认；成功后返回 10 个一次性恢复码。**恢复码只在启用或重生成恢复码的那次响应里返回明文**，之后数据库只存其 SHA-256 哈希。
- `disable` 与 `recovery-codes` 都要求当前密码与有效验证码（或恢复码）；重生成后旧恢复码立即失效。
- 这些接口都在会话认证之后，继承同源校验、`Cache-Control: no-store` 等既有约定。
- 启用、关闭与重生成恢复码分别记审计动作 `admin.totp.enable`、`admin.totp.disable`、`admin.totp.recovery`；审计与日志不含密钥、验证码与恢复码。

### API 客户端兼容性

- 只发送用户名与密码的客户端，在目标账号启用两步验证后会收到 `{totp_required: true, challenge, expires_at}` 而不是 Cookie，必须再调用 `POST /auth/login/totp` 完成第二步，否则无法建立会话。
- `totp_required` 是响应字段，HTTP 状态仍为 `200`；客户端应先判断它，而不是把登录成功等同于拿到 Cookie。
- 未启用两步验证的账号行为与之前完全一致；`admin.totp_enabled` 与 `/auth/me` 的 `totp_enabled` 可用于提前判断。

## 仪表盘与系统

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/dashboard` | 汇总、在线数、30 天流量、系统信息 |
| GET | `/system` | CPU/内存/运行时长/版本/会话数 |

```json
{
  "version": "1.0.0",
  "uptime_seconds": 3600,
  "inbounds": 1,
  "clients": 3,
  "enabled_inbounds": 1,
  "enabled_clients": 2,
  "online_ips": 4,
  "sessions": 6,
  "up_bytes": 123456,
  "down_bytes": 654321,
  "traffic_30d": [{ "day": "2026-01-01", "up_bytes": 1, "down_bytes": 2 }],
  "attention": {
    "expiring_soon": 2, "expired": 1, "quota_exceeded": 0, "idle": 7,
    "expiring_window_days": 7
  },
  "system": {
    "os": "linux", "arch": "amd64", "num_cpu": 2, "goroutines": 42,
    "cpu_percent": 3.5, "mem_used": 104857600, "mem_total": 2147483648,
    "heap_alloc": 8388608, "host_metrics": true
  }
}
```

`attention` 是仪表盘「需要关注」的四个数字：

| 字段 | 含义 | 对应列表 |
|---|---|---|
| `expiring_soon` | 启用且 `expiring_window_days`（7）天内到期 | `/clients?status=expiring` |
| `expired` | 启用但已过期 | `/clients?status=expired` |
| `quota_exceeded` | 启用且已超配额 | `/clients?status=quota_exceeded` |
| `idle` | 启用且累计流量为 0（从未连接过） | `/clients?status=idle` |

四个数字与对应列表的 `total` 由同一段 SQL 谓词求出，因此卡片与点进去的条数一致；`idle` 也因此包含刚创建但还没连过的用户，这是它的本意（预配了却没用）。

## 数据时效

- `traffic_30d` 来自按天汇总表 `traffic_daily_totals`，由运行时在落库（默认每 10 秒）的同一事务内维护，因此与用户明细始终一致；窗口为最近 30 天。
- `up_bytes` / `down_bytes` 在仪表盘中是内存实时累计值，用户列表会叠加实时计数，而入站列表读的是已落库值——三者的差别只有一个落库周期。
- 用户列表的 `status` 筛选在 SQL 中按**已落库**计数判断，而行内展示的 `status` 用叠加了实时计数的值。客户端越过配额时会立刻触发一次落库（见 `onClientExceeded`），所以两者不一致的窗口通常远小于一个落库周期；即使偶发，`total` 与 `items` 仍来自同一次查询，不会出现"筛选结果里有 status 不匹配的行"以外的偏差。
- 删除用户或入站时，其已落库的日流量会随明细一起从汇总表中扣除。

## 入站

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/inbounds` | 列表，附带 `methods` 支持的方法 |
| POST | `/inbounds` | 创建 |
| GET | `/inbounds/:id` | 详情 |
| PUT | `/inbounds/:id` | 更新 |
| DELETE | `/inbounds/:id` | 删除（级联删除用户与流量记录） |
| POST | `/inbounds/:id/toggle` | body `{"enabled": true\|false}`，**必填**，缺失返回 `400` |
| POST | `/inbounds/:id/restart` | 重建监听器 |
| GET | `/inbounds/:id/clients` | 该入站的用户 |
| POST | `/inbounds/:id/clients` | 在该入站创建用户 |

入站请求体：

```json
{
  "remark": "香港 8388",
  "listen": "0.0.0.0",
  "port": 8388,
  "method": "2022-blake3-aes-128-gcm",
  "server_psk": "",
  "domain": "hk.example.com",
  "domain_port": 0,
  "udp_enabled": true,
  "enabled": true
}
```

- `server_psk` 留空时自动生成；SS2022 多用户方法必须为对应长度的 base64。
- 单用户方法（`2022-blake3-chacha20-poly1305`）的 `server_psk` 必须为空，且入站只能有一个启用用户。
- 端口冲突返回 `409 port_in_use`；权限不足返回 `403 permission_denied`；地址非法返回 `400 invalid_address`。调用方应按 `code` 分支，而不是按 HTTP 状态码猜原因。
- `domain` 与 `domain_port` 可选，用于让该入站使用独立的分享域名（见下节）。
- 列表与详情在入站**已启用但未能启动**（端口被占用、权限不足等）时带 `last_error` 说明原因；字段缺省表示运行时与记录一致。

### 入站详情

`GET /inbounds/:id/detail?days=30` 返回单个入站的运行视图：流量趋势、最忙用户、状态分布与最近失败访问。

| 参数 | 规则 |
|---|---|
| `days` | 1–90，默认 30；按有效时区计算，`daily` 恒为 N 天 |

```json
{
  "inbound": { "id": 1, "remark": "main", "...": "完整 InboundView，含 client_count / up_bytes / online_ips / last_error" },
  "days": 30,
  "daily": [{ "day": "2026-01-01", "up_bytes": 1, "down_bytes": 2 }],
  "top_clients": [{ "id": 1, "name": "alice", "up_bytes": 10, "down_bytes": 20, "enabled": true }],
  "status_counts": { "active": 1, "disabled": 0, "expired": 0, "quota_exceeded": 0 },
  "recent_failures": [],
  "access_recording": false
}
```

- `daily` 来自 `traffic_daily` 与 `clients` 的连接，**只统计该入站的用户**，并按 N 天补零（与仪表盘、访问分析一致的横轴语义）。
- `top_clients` 最多 5 个，按累计字节降序。
- `status_counts` 的四态与 `/clients?status=` 用同一段谓词，数字与筛选结果一致。
- `recent_failures` 是该入站最近 20 条**拦截或失败**的访问记录（不含放行），按时间倒序；`access_recording` 为 `false` 时该数组恒为空，页面据此区分「没有失败」与「没有记录」。
- 入站不存在返回 `404 not_found`；`days` 越界返回 `400 invalid_request`。

### 关联域名

多入站部署过去只能通过全局 `public_host` 指定一个主机名，所有入站的分享链接都用它。现在每个入站可以单独关联域名：

| 字段 | 说明 |
|---|---|
| `domain` | 主机名或裸 IP，**不含**协议、端口与方括号。保存时统一为小写并去掉末尾的点，非法值返回 `400` |
| `domain_port` | 该域名的端口，`0` 表示跟随全局端口再回退到入站端口。填写端口时必须同时填写域名 |

分享链接与订阅的主机/端口解析顺序（越靠前越优先）：

| | 主机 | 端口 |
|---|---|---|
| 1 | 入站 `domain` | 入站 `domain_port` |
| 2 | 全局 `public_host` | 全局 `public_port` |
| 3 | 请求级 `?host=`（仅 `/clients/:id/link`） | 入站 `port` |
| 4 | 监听地址推导（`0.0.0.0` → `127.0.0.1`，`::` → `::1`） | |

- 入站域名优先于全局设置：它是唯一能描述单个入站的配置。
- **未设置 `domain` 的入站行为与之前完全一致**，链接输出不变。
- 该功能只影响链接渲染，不涉及监听地址、协议或 TLS；数据面不会因此重载。
- 域名不要求唯一：不同入站可以在不同端口使用同一域名。

```bash
# 给入站绑定域名后，链接立即变成 ss://…@hk.example.com:8443
curl -b cookies.txt -X PUT http://127.0.0.1:2096/api/v1/inbounds/1 \
  -H 'Content-Type: application/json' \
  -d '{"remark":"香港","listen":"0.0.0.0","port":8388,"method":"2022-blake3-aes-128-gcm","domain":"hk.example.com","domain_port":8443,"udp_enabled":true,"enabled":true}'
```

入站响应额外字段：`client_count`、`enabled_client_count`、`up_bytes`、`down_bytes`、`online_ips`。

口径说明：

- `up_bytes` / `down_bytes` 是**已落库**的累计值，由运行时每 10 秒写入一次；`GET /clients` 与 `GET /dashboard` 会在落库值之上叠加内存中的实时计数，因此入站流量最多可能比它们滞后一个落库周期。需要即时数值请用仪表盘或用户详情。
- `online_ips` 是"该入站内去重后的来源 IP 数"，与仪表盘的 `online_ips`（各用户去重 IP 数之和）口径不同：多个用户共用同一出口 IP 时，前者计 1，后者按用户分别计数。

## 用户

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/clients?inbound_id=&status=&q=&limit=&offset=` | 分页列表（见下） |
| GET | `/inbounds/:id/clients?status=&q=&limit=&offset=` | 该入站的分页列表，参数同上 |
| GET | `/clients/:id` | 详情 |
| PUT | `/clients/:id` | 更新 |
| DELETE | `/clients/:id` | 删除 |
| POST | `/clients/:id/toggle` | body `{"enabled": true\|false}`，**必填**，缺失返回 `400`（避免空 body 把用户关掉） |
| POST | `/clients/:id/reset` | 清零已用流量 |
| POST | `/clients/:id/token` | 轮换订阅 token |
| POST | `/clients/:id/kick` | 断开该用户全部连接 |
| GET | `/clients/:id/link` | 分享链接与订阅路径 |
| GET | `/clients/:id/usage?days=30` | 每日流量（`days` 取 1–365，返回恰好 N 天） |

用户请求体：

```json
{
  "name": "alice",
  "psk": "",
  "enabled": true,
  "quota_bytes": 107374182400,
  "expires_at": 0,
  "ip_limit": 0,
  "reset_policy": "monthly",
  "reset_day": 0,
  "note": ""
}
```

- `psk` 语义按方法区分：**创建**（`POST`）时留空表示自动生成；**更新**（`PUT`）时留空表示保留现有 PSK，不能用来清空密码。传统 AEAD 方法接受任意非空密码，SS2022 要求 base64 且长度匹配。
- 单用户方法（`2022-blake3-chacha20-poly1305`）的每个入站最多只能有一个启用用户；创建、更新、`toggle` 与导入都会校验，改方法时若已有多个启用用户也会返回 `400`。
- `quota_bytes=0` 不限；`expires_at=0` 永不过期；`ip_limit=0` 不限。
- `reset_policy` 为 `none` 或 `monthly`；`reset_day` 为 `0`（跟随全局）或 `1–28`。

### 用户列表（分页）

`GET /clients` 与 `GET /inbounds/:id/clients` 在 SQL 中完成筛选与分页，一次只返回一页：

| 参数 | 默认 | 规则 |
|---|---|---|
| `inbound_id` | 全部 | `>0` 只返回该入站；`0`/省略=全部。仅顶层 `/clients` 有效，`/inbounds/:id/clients` 由路径决定并忽略此参数 |
| `status` | `all` | `all`、`active`、`disabled`、`expired`、`quota_exceeded`，以及两个**仅用于筛选**的值 `expiring`（启用且 7 天内到期）与 `idle`（启用且累计流量为 0） |
| `q` | 空 | 去空白后按子串匹配 `name`、`note`、入站 `remark`、`method`；最长 100 字符 |
| `limit` | `100` | `1–500` |
| `offset` | `0` | `0–10000000` |

响应：

```json
{ "items": [], "total": 5000, "limit": 100, "offset": 0 }
```

`limit` / `offset` 回显实际生效值，`total` 是满足筛选条件的总行数（与 `items` 由同一份 `WHERE` 求出）。

- 参数校验是**拒绝**而不是截断：`limit=0`、`limit=501`、`offset=-1`、`status=bogus`、`inbound_id=abc`、超长 `q` 都返回 `400 invalid_request`，避免调用方以为自己拿到了全量数据。
- `offset` 超出总数返回 `200` + 空 `items` + 正确的 `total`。
- 排序固定为 `id` 升序，翻页不跳行不重复。
- `expiring` / `idle` 是**筛选值而非状态**：行内 `status` 字段永远只会是 `active`/`disabled`/`expired`/`quota_exceeded` 四者之一。它们存在的目的是让仪表盘「需要关注」的卡片能深链到对应的行（`/clients?status=expiring`），数字与列表 `total` 由同一段 SQL 谓词求出，不会各不相同。
- `q` 里的 `%`、`_`、`\` 按字面量匹配（内部使用 `LIKE ... ESCAPE '\'`）。旧版面板在浏览器里把四个字段拼成一个串再匹配，因此**跨字段的短语（如 `alice 香港`）不再命中**，这是有意的简化。
- 只读 `clients` 的展示列，并从 `inbounds` JOIN 出 `remark`/`method`，所以列表既不读也不返回任何 PSK。

成本随**用户总数**而非返回行数增长：`total` 是精确计数，一次请求必须扫过所有满足条件的行。实测（单机 SQLite，5 入站）：

| 请求 | 5000 用户 | 50000 用户 |
|---|---|---|
| `?limit=20` | 0.31 ms | 2.4 ms |
| `?limit=20&status=active` | 0.68 ms | 7.0 ms |
| `?limit=20&q=…` | 3.8 ms | 41.7 ms |
| `GET /inbounds`（全表聚合） | 0.98 ms | 11.6 ms |

本项目目标规模是数千用户，这些延迟都在 1 ms 量级，因此计数与入站聚合**没有加缓存**：缓存会引入失效逻辑，并让刚重置流量的用户看到最多一个缓存周期的陈旧合计。若用户数增长到数万且这些端点成为瓶颈，再考虑缓存或近似计数。

> 兼容性说明：早期版本的 `GET /clients` 一次返回全部用户。现在默认只返回 100 条，上限 500 条。需要全量数据请用 `GET /export/clients`（CSV，含 PSK），或按 `limit`/`offset` 翻页。

`GET /clients` 与 `GET /inbounds/:id/clients` 返回**摘要**，`GET /clients/:id` 返回**详情**。两者共用以下计算字段：

```json
{
  "status": "active",
  "used_bytes": 1024,
  "remaining_bytes": 1048576,
  "online_ips": ["203.0.113.5"],
  "inbound_remark": "香港 8388",
  "method": "2022-blake3-aes-128-gcm"
}
```

`status` 取值：`active`、`disabled`、`expired`、`quota_exceeded`。

列表摘要**不包含**凭据与派生字段：`psk`、`sub_token`、`link`、`subscription_url`，以及列表不展示的 `created_at`、`updated_at`、`last_reset_at`。这样批量响应里不含任何秘密。需要这些字段时按单个用户取：

- 详情：`GET /clients/:id`，返回摘要字段 + `psk`、`sub_token`、`link`、`subscription_url` 与上述时间戳；
- 只要链接：`GET /clients/:id/link`。

> 兼容性说明：早期版本的列表条目包含 `psk`/`sub_token`/`link`，依赖这些字段的脚本请改调详情端点。

### 用户流量

`GET /clients/:id/usage?days=30` 返回该用户的每日流量，按日期升序：

```json
{ "items": [{ "day": "2026-01-01", "up_bytes": 1024, "down_bytes": 2048 }] }
```

数据来自已落库的 `traffic_daily`，随落库周期（默认 10 秒）更新，因此最多滞后一个周期。
窗口按有效时区计算，`days=N` 返回恰好 N 个日桶（含今天）；`days` 非法或越界返回 `400 invalid_request`。仪表盘的 `traffic_30d` 同一语义。

### 导入 / 导出

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/export/clients?inbound_id=` | 导出用户 CSV（`inbound_id` 省略时导出全部；非数字或负数返回 `400 invalid_request`，不会退化成导出全部凭据） |
| POST | `/inbounds/:id/clients/import` | 从 CSV 或 `ss://` 链接批量导入 |

> 导出接口位于 `/export/clients` 而不是 `/clients/export`：与 `/clients/:id` 的路径通配同层会产生路由冲突。

导出响应为 `text/csv; charset=utf-8`，带 `Content-Disposition: attachment` 与 `Cache-Control: no-store`。列为：

```
inbound_id,inbound_remark,name,psk,enabled,quota_bytes,expires_at,ip_limit,reset_policy,reset_day,note
```

**文件包含用户 PSK**（否则无法恢复用户），请按凭证妥善保管。

导入请求体：

```json
{
  "format": "auto",
  "content": "name,psk\nalice,\nbob,\n",
  "defaults": { "enabled": true, "quota_bytes": 0, "reset_policy": "none" }
}
```

- `format`：`auto`（默认）、`csv`、`ss`。`auto` 按首行判断：以 `ss://` 开头为链接，否则按 CSV。
- CSV 首行含 `name` 列时按列名映射，否则按固定顺序
  `name,psk,enabled,quota_bytes,expires_at,ip_limit,reset_policy,reset_day,note`。
  导出的文件可直接导回（表头含 `inbound_id`/`inbound_remark`，导入时忽略）。
- `ss://` 每行一条，**方法必须与目标入站一致**，否则该行失败。
- `psk` 留空自动生成；单次最多 1000 行。
- 响应为**部分成功**语义：

```json
{ "created": 2, "failed": [{ "line": 3, "name": "bob", "error": "name already exists in this inbound" }] }
```

- `failed[].line` 是**粘贴文本中的原始行号**（1 起），中间的空行不会让后续行号前移。
- 只有参数错误（内容为空、超行数上限、入站不存在）才返回 4xx；行级失败属于正常结果，逐行给出原因，因此没有单独的「跳过数」汇总字段。
- 整个批次只重建一次入站监听，不会因用户数增加而反复重建。

## 审计日志

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/audit` | 查询审计记录（需要会话） |

查询参数：`limit`（默认 100，最大 500）、`offset`、`action`、`username`、`since`/`until`（Unix 秒，`since` 含、`until` 不含）。

`action` 与 `username` 都有索引（`(action, created_at)`、`(username, created_at)`）。`total` 仍是对匹配行精确计数，所以按操作人筛选的耗时随**该操作人的记录数**线性增长；按不存在的用户名查询则只做一次索引定位。默认保留 90 天，随保留期增长请以 `total` 判断是否需要收窄时间范围。

```json
{
  "items": [{
    "id": 12, "created_at": 1790000000, "admin_id": 1, "username": "admin",
    "action": "client.create", "target_type": "client", "target_id": 3,
    "detail": "inbound_id=1 name=\"alice\" enabled=true quota_bytes=0",
    "ip": "203.0.113.5"
  }],
  "total": 1, "limit": 100, "offset": 0,
  "actions": ["auth.login", "auth.login.failed", "client.create", "..."]
}
```

记录范围：

| 动作 | 说明 |
|---|---|
| `auth.login` / `auth.login.failed` / `auth.logout` | 登录成功、失败（含尝试的用户名；第二步失败带 `stage=totp`）与退出 |
| `admin.password` | 修改管理员密码 |
| `admin.reauth.failed` | 重认证（改密、两步验证相关操作）输入了错误的当前密码或验证码 |
| `admin.totp.enable` / `.disable` / `.recovery` | 启用/关闭两步验证、重生成恢复码 |
| `inbound.create` / `.update` / `.delete` / `.toggle` / `.restart` | 入站变更 |
| `client.create` / `.update` / `.delete` / `.toggle` / `.reset` / `.token` / `.kick` | 用户变更 |
| `client.import` / `client.export` | 导入导出 |
| `client.batch` | 批量用户操作（动作、选中/成功/失败数、前 20 个 id） |
| `export.audit` / `export.access` | 导出审计日志 / 访问明细（只记字节数与是否截断） |
| `settings.update` | 系统设置变更 |

- 只记录**成功**的操作；两个例外：登录失败记 `auth.login.failed`，重认证失败记 `admin.reauth.failed`（连续失败达到阈值后返回 429）。
- `detail` 为固定的 `key=value` 摘要，**不包含**密码、用户 PSK、服务器 PSK、订阅 token、TOTP 密钥、验证码与恢复码。
- 保留策略：90 天，且最多 5 万条（超出按时间删除最旧记录）。
- 查询参数 `limit`（1–500）与 `offset`、`since`/`until`（Unix 秒，左闭右开）非法时返回 `400 invalid_request`，与用户列表和访问明细一致，不会静默回退到默认页。

## 设置

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/settings` | 读取 |
| PUT | `/settings` | 更新 |

```json
{
  "public_host": "node.example.com",
  "public_port": 0,
  "subscription_enabled": true,
  "allow_private_targets": false,
  "reset_day": 1,
  "timezone": "Asia/Shanghai",
  "access_log_enabled": false
}
```

- `public_host` / `public_port` 用于生成分享链接；留空/0 时回退到入站监听地址与端口。写入前会规范化（去首尾空白、小写、去掉一个末尾点），因此 `" Example.COM. "` 存成 `example.com`。
- `allow_private_targets` 立即生效；云元数据地址始终拒绝。
- `timezone` **必填**，必须是 IANA 时区名或 `Local`；空串返回 `400 invalid_request`（否则 `time.LoadLocation("")` 会静默切到 UTC）。
- `access_log_enabled` 控制是否记录代理访问目标，默认 `false`；改动立即生效、无需重启。
- **`PUT /settings` 是整体替换而非部分更新**：请求体必须带上全部字段。缺少字段会被解码为零值（例如漏掉 `subscription_enabled` 会让订阅链接全部 404），因此客户端应先 `GET` 再整体提交。`public_port` 0–65535、`reset_day` 0–28 越界同样返回 400。

## 访问分析

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/access/summary` | 窗口汇总、Top 目标、Top 用户与每日趋势 |
| GET | `/access/events` | 原始访问明细（分页、可筛选） |

`/access/summary` 参数：

| 参数 | 说明 |
|---|---|
| `days` | 1–90，默认 7；窗口按有效时区换算 |
| `client_id` | 可选，只看一个用户 |
| `inbound_id` | 可选，只看一个入站 |

```json
{
  "enabled": true,
  "days": 7,
  "raw_retention_days": 7,
  "aggregate_retention_days": 90,
  "dropped_records": 0,
  "total_hits": 12,
  "allowed_hits": 10,
  "blocked_hits": 1,
  "failed_hits": 1,
  "unique_hosts": 4,
  "up_bytes": 1234,
  "down_bytes": 5678,
  "daily": [{ "day": "2026-11-15", "hits": 12, "allowed_hits": 10, "blocked_hits": 1, "failed_hits": 1, "up_bytes": 1234, "down_bytes": 5678 }],
  "top_hosts": [{ "host": "example.com", "hits": 8, "blocked_hits": 0, "up_bytes": 100, "down_bytes": 200 }],
  "top_clients": [{ "client_id": 1, "name": "alice", "inbound_id": 1, "inbound_remark": "main", "hits": 8, "up_bytes": 100, "down_bytes": 200 }]
}
```

`/access/events` 参数：`limit`（1–500，默认 100）、`offset`（0–100000）、`client_id`、`inbound_id`、`outcome`（`all`/`allowed`/`blocked`/`failed`）、`network`（`all`/`tcp`/`udp`）、`q`（目标子串，≤128 字符）、`since`/`until`（Unix 秒，左闭右开）、`since_day`/`until_day`（`YYYY-MM-DD`，按有效时区换算成秒，含首含尾；与 `since`/`until` 同时出现时以此为准）。

```json
{
  "items": [{
    "id": 1,
    "started_at": 1794714231,
    "duration_ms": 40,
    "client_id": 1,
    "client_name": "alice",
    "inbound_id": 1,
    "inbound_remark": "main",
    "network": "tcp",
    "host": "example.com",
    "port": 443,
    "ip": "93.184.216.34",
    "outcome": "allowed",
    "reason": "",
    "up_bytes": 100,
    "down_bytes": 200
  }],
  "total": 1,
  "limit": 100,
  "offset": 0
}
```

- `host` 是客户端请求的目标（域名或 IP 字面量），`ip` 是实际拨号的地址；被拦截或解析失败时 `ip` 为空。
- `daily` 恒为窗口内**每天一行**（没有流量的天补 0），因此图表横轴不会跳过安静的日子；`allowed_hits` / `blocked_hits` / `failed_hits` 与顶层的三个总数同源，因此趋势线和卡片不会互相矛盾。
- 聚合表每个 `(天, 用户, 入站)` 最多保留 **256** 个不同目标（`internal/model.AccessMaxHostsPerKey`），溢出部分折叠为一行 `host = "<other>"`；因此 `unique_hosts` 是折叠后的计数，成交与字节总数不受影响。明细列表里不会出现这一行。
- `outcome`：`allowed`（已连接）、`blocked`（门禁或策略拒绝，未拨号）、`failed`（已尝试但失败）。
- `reason`：`disabled`、`expired`、`quota`、`ip_limit`、`sessions`（用户侧门禁）；`policy`（内网/元数据/面板地址等目标限制）；`resolve`（域名解析失败，含域名不存在与解析超时）；`dial`（连接目标失败）。
- `client_name` / `inbound_remark` 为空表示该用户或入站已被删除，历史仍保留到保留期结束。
- TCP：每条连接一条记录（含被拒绝的连接）。UDP：按 NAT 会话中的目标聚合，一个活跃目标在安静 2 秒后或最长 10 秒窗口结束时写一条记录，会话结束时不再重复已上报的字节；被策略拒绝的 UDP 目标每个会话只记一条。
- UDP 的上行字节是**已提交给内核**的字节数（`WriteToUDPAddrPort` 成功返回），不是对端确认收到的字节数；发送失败的数据报不计入。
- 记录尽力而为：写入队列有界（8192），队列满、UDP 单会话目标数超过上限（256）、落库失败或进程退出时队列里残留的记录都会丢弃并计入 `dropped_records`，**不会阻塞转发或计费**。
- 明细保留 7 天、按天聚合保留 90 天，由面板后台任务**每分钟**清理一次（单次最多约 500 ms 工作量，积压分摊到后续 tick；不再是一小时一次，否则清理速度跟不上写入速度）。聚合直接从 `access_daily` 读取，因此 90 天窗口不需要扫描明细。
- 关闭 `access_log_enabled` 后不再产生新记录：**已建立的 TCP 连接与正在进行的 UDP 会话**会先冲刷已计数的字节再停止计数，关闭后传输的字节不再归属到访问记录。关闭前的历史仍可读取（`enabled=false` 供前端提示）。
- 开启 `access_log_enabled` 后，**新建的连接**开始记录；开关之前就已经建立的 TCP 连接不会补记（它没有对应的跟踪条目），而仍在活动的 UDP NAT 会话会从下一个数据包开始继续上报。这样「开关关闭时数据面零开销」与「开关对新建连接立即生效」可以同时成立。
- 记录**不含**载荷内容、URL 路径和客户端来源 IP。

- `total` 在匹配行超过 100001 条时是下界（等于 `offset` 上限加一），因为更靠后的页已经取不到；窗口内小于该值时是精确值。
- 目标名会规范化（去空白、小写、去掉一个末尾点、截断到 255 字节），因为它是客户端可控的字符串。

参数非法（`days`/`limit`/`offset` 越界、未知 `outcome`/`network`、`q` 过长、`until<=since`、`since_day`/`until_day` 不是合法日期或顺序颠倒）返回 `400 invalid_request`；未登录返回 `401`。

## 批量操作

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/batch/clients` | 对一组用户执行同一动作 |

> 位于 `/batch/clients` 而不是 `/clients/batch`：后者会与 `/clients/:id` 的通配同层冲突（gin 会 panic）。

```json
{ "action": "extend", "ids": [1, 2, 3], "days": 30 }
```

| 字段 | 规则 |
|---|---|
| `action` | `enable`、`disable`、`delete`、`extend`、`add_quota`、`reset` |
| `ids` | 1–500 个正整数，重复项自动去重；空数组、0、负数、超上限都返回 `400` |
| `days` | 仅 `extend` 使用，1–3650 |
| `bytes` | 仅 `add_quota` 使用，1–2^50 |

响应为**部分成功**语义，与导入一致：

```json
{ "action": "enable", "applied": 3, "failed": [{ "id": 7, "name": "bob", "error": "user not found" }] }
```

行为约定：

- **按入站分组**：每个入站一个事务、一次监听重建。选中 N 个用户不会造成 N 次重建（可用 `ReloadCount` 观测），这一点与批量导入相同。
- `enable` 在**单用户方法**的入站上会整组拒绝（`该入站为单用户方法，一次只能启用一个用户`）：批量启用可能让同一入站出现两个启用用户，逐行检查无法发现这种组合。其它入站不受影响。
- `extend` 对 `expires_at = 0`（永不过期）的用户记失败 `never expires, nothing to extend`；`add_quota` 对 `quota_bytes = 0`（不限流量）的用户记失败 `unlimited traffic, nothing to add`。**不会**把一个「不限」的账号悄悄改成「有限」。
- `delete` 会一并回收运行时计量账户并从按天汇总中扣除该用户的流量（与单个删除同一条路径）。
- `reset` 是唯一**逐用户**执行的动作用户：它必须和内存计数器在同一把锁里归零（否则下一次落库会把旧计数写回来），因此每个用户一次事务，而不是一条批量 SQL。
- 不存在的 id 记为该 id 失败，其余照常执行。
- 事务成功但监听重建失败时，返回 500 并说明「已保存但未能重建」，此时需要重启该入站修复；这是唯一会出现的半一致状态。
- 整批写一条审计 `client.batch`，内容为动作、选中数、成功数、失败数与前 20 个 id。

## 导出

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/export/clients?inbound_id=` | 用户 CSV（含 PSK），见上文 |
| GET | `/export/audit` | 审计日志 CSV |
| GET | `/export/access` | 访问分析明细 CSV |

两个新导出复用各自列表端点的筛选参数，因此**同样的查询导出的行与页面显示的行一致**：

| 端点 | 支持参数 |
|---|---|
| `/export/audit` | `action`、`username`、`since`/`until`（Unix 秒，左闭右开） |
| `/export/access` | `client_id`、`inbound_id`、`outcome`、`network`、`q`、`since`/`until`、`since_day`/`until_day` |

- **不接受 `limit` / `offset`**：导出的是筛选结果的整体，不是某一页；带上它们返回 `400 invalid_request`，避免文件只是第 3 页而调用方以为拿到了全部。
- 响应 `text/csv; charset=utf-8`、`Content-Disposition: attachment`、`Cache-Control: no-store`。
- 行数上限：审计 `50000`（等于审计表行数上限，实际不会触发）、访问明细 `100000`。**触发上限时**响应头带 `X-Export-Truncated: true`，文件本身仍是干净 CSV（不插入说明行）。超过上限的部分按时间**由新到旧**截断，即保留最近的记录。
- 列：
  - 审计：`created_at,created_at_local,username,action,target_type,target_id,detail,ip`
  - 访问：`started_at,started_at_local,client_id,client_name,inbound_id,inbound_remark,network,host,port,ip,outcome,reason,up_bytes,down_bytes,duration_ms`
- `created_at_local` / `started_at_local` 按**有效时区**格式化为 `YYYY-MM-DD HH:MM:SS`，同时保留原始 Unix 秒，避免导出文件需要额外的时区知识才能读。
- 两个导出都会写一条审计记录（`export.audit` / `export.access`），只记字节数与是否截断，不记录筛选内容里的敏感值。
- 访问明细导出与页面同样受保留期限制（明细 7 天）；**开关关闭时导出的是开启期间的历史**，不是错误。文件含访问目标与解析后的 IP，属敏感数据。

## 备份

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/backup` | 下载 `sspanel-YYYYMMDD-HHMMSS.db` 快照 |

`VACUUM INTO` 在单个读事务内读取整库，因此快照本身一致，无需暂停写入：备份期间 WAL 允许并发读，流量落库与面板写操作都继续执行。代价是快照描述的是某一时刻的一致状态，不一定包含请求那一刻刚排队的写入。

恢复不支持在线进行，需要停机替换数据库文件，见 [deployment.md](deployment.md)。

## 订阅

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/sub/:token` | 无 Cookie；返回 base64 列表、Clash YAML 或 sing-box JSON |

格式选择：

| 方式 | 结果 |
|---|---|
| 默认 | base64 编码的 `ss://` 列表（每行一条） |
| UA 含 `clash`/`meta`/`mihomo`，或 `format=clash` | Clash / mihomo YAML |
| UA 含 `sing-box`，或 `format=singbox`（也接受 `sing-box`） | sing-box 远程配置 JSON |

响应头：

| 头 | 值 |
|---|---|
| `Subscription-Userinfo` | `upload=<已用上行>; download=<已用下行>; total=<配额>; expire=<到期 Unix 秒>` |
| `Profile-Title` | `base64:<base64(用户名)>`（空名字时为 `sspanel`） |
| `Profile-Update-Interval` | `12`（小时） |
| `Cache-Control` / `Pragma` | `no-store` / `no-cache` |

- `total=0` 表示**不限流量**，`expire=0` 表示**永不过期**，二者始终显式输出为 0 而不是省略字段；Clash、mihomo、Stash、sing-box 都按这个约定渲染「已用 / 总量、到期时间」。
- `upload` / `download` 是**已落库**的累计值，随落库周期（默认 10 秒）更新，最多滞后一个周期。
- `Profile-Title` 用 `base64:` 前缀是为了让中文节点名能完整穿过 HTTP 头；不识别该前缀的客户端会原样显示前缀后的串，不影响连接。
- 用户被禁用、已到期、超配额，或订阅功能关闭时返回 `404`；**404 不携带上述账号头**，避免从响应头判断 token 是否存在。
- 响应体与 `Subscription-Userinfo` 都含账号信息，因此响应一律 `no-store`（404 也带同样的头，避免中间缓存留存）。
- sing-box 输出形如 `{"outbounds":[{"type":"shadowsocks","tag":"<用户名>","server":"...","server_port":8388,"method":"...","password":"..."}]}`；Shadowsocks 2022 的 `password` 为 `serverPSK:clientPSK`，传统 AEAD 为客户端 PSK。同名客户端会追加 ` (2)` 之类后缀，保证 tag 唯一（sing-box 拒绝重复 tag）。

## 健康检查

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/healthz` | 无认证，返回 `{"status":"ok","version":"..."}` |

## 示例

```bash
# 登录并保存 Cookie
curl -c cookies.txt -X POST http://127.0.0.1:2096/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"your-password"}'

# 创建入站
curl -b cookies.txt -X POST http://127.0.0.1:2096/api/v1/inbounds \
  -H 'Content-Type: application/json' \
  -d '{"remark":"test","listen":"0.0.0.0","port":8388,"method":"2022-blake3-aes-128-gcm","udp_enabled":true,"enabled":true}'

# 创建用户
curl -b cookies.txt -X POST http://127.0.0.1:2096/api/v1/inbounds/1/clients \
  -H 'Content-Type: application/json' \
  -d '{"name":"alice","enabled":true,"quota_bytes":0}'

# 获取分享链接
curl -b cookies.txt http://127.0.0.1:2096/api/v1/clients/1/link
```
