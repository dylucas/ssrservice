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

- 时间字段为 UTC Unix 秒（`expires_at`、`created_at` 等）；前端按本地时区展示。
- 会话 Cookie 名 `sspanel_session`，`HttpOnly`、`SameSite=Strict`，HTTPS 时带 `Secure`。
- 变更请求若带 `Origin` 头，必须与 `Host` 同源，否则返回 `403 invalid_origin`。
- 登录失败限制：默认 5 次/分钟/IP，超出返回 `429 too_many_attempts`。真实客户端 IP 只在请求来自 `panel.trusted_proxies` 时采用 `X-Forwarded-For`。
- `/api/v1` 的 JSON 请求体上限为 1 MiB，超出返回 `413 payload_too_large`。
- 面板静态资源在请求带 `Accept-Encoding: gzip` 时返回 `Content-Encoding: gzip`，并始终带 `Vary: Accept-Encoding`；`HEAD` 与 `Range` 请求返回未压缩文件。API 的 JSON 响应不压缩。
- `/api/v1` 的全部响应带 `Cache-Control: no-store` 与 `Pragma: no-cache`：响应可能包含用户 PSK、订阅 token 等信息，不允许任何缓存保存副本。
- 只有 `GET /clients` 与 `GET /inbounds/:id/clients` 两个列表端点在响应体 ≥ 4 KiB 且请求带 `Accept-Encoding: gzip` 时返回 `Content-Encoding: gzip`（这两个响应不含凭据，压缩没有 BREACH 类风险）。其余 API 响应不压缩；所有可能被压缩的响应都带 `Vary: Accept-Encoding`。

## 认证

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/auth/login` | body `{username, password}`，成功后 Set-Cookie |
| POST | `/auth/logout` | 清除会话 |
| GET | `/auth/me` | 当前管理员信息 |
| PUT | `/auth/password` | body `{current_password, new_password}`，修改后轮换会话 |

登录响应：

```json
{
  "admin": { "id": 1, "username": "admin", "must_change_password": false },
  "expires_at": 1790000000
}
```

`must_change_password=true` 时，除 `/auth/me`、`/auth/password`、`/auth/logout` 外的接口返回
`403 password_change_required`。

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
  "system": {
    "os": "linux", "arch": "amd64", "num_cpu": 2, "goroutines": 42,
    "cpu_percent": 3.5, "mem_used": 104857600, "mem_total": 2147483648,
    "heap_alloc": 8388608, "host_metrics": true
  }
}
```

## 数据时效

- `traffic_30d` 来自按天汇总表 `traffic_daily_totals`，由运行时在落库（默认每 10 秒）的同一事务内维护，因此与用户明细始终一致；窗口为最近 30 天。
- `up_bytes` / `down_bytes` 在仪表盘中是内存实时累计值，用户列表会叠加实时计数，而入站列表读的是已落库值——三者的差别只有一个落库周期。
- 删除用户或入站时，其已落库的日流量会随明细一起从汇总表中扣除。

## 入站

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/inbounds` | 列表，附带 `methods` 支持的方法 |
| POST | `/inbounds` | 创建 |
| GET | `/inbounds/:id` | 详情 |
| PUT | `/inbounds/:id` | 更新 |
| DELETE | `/inbounds/:id` | 删除（级联删除用户与流量记录） |
| POST | `/inbounds/:id/toggle` | body `{enabled}` |
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
  "udp_enabled": true,
  "enabled": true
}
```

- `server_psk` 留空时自动生成；SS2022 多用户方法必须为对应长度的 base64。
- 单用户方法（`2022-blake3-chacha20-poly1305`）的 `server_psk` 必须为空，且入站只能有一个启用用户。
- 端口冲突返回 `409 conflict`；权限不足/地址非法返回 `400`。

入站响应额外字段：`client_count`、`enabled_client_count`、`up_bytes`、`down_bytes`、`online_ips`。

口径说明：

- `up_bytes` / `down_bytes` 是**已落库**的累计值，由运行时每 10 秒写入一次；`GET /clients` 与 `GET /dashboard` 会在落库值之上叠加内存中的实时计数，因此入站流量最多可能比它们滞后一个落库周期。需要即时数值请用仪表盘或用户详情。
- `online_ips` 是"该入站内去重后的来源 IP 数"，与仪表盘的 `online_ips`（各用户去重 IP 数之和）口径不同：多个用户共用同一出口 IP 时，前者计 1，后者按用户分别计数。

## 用户

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/clients?inbound_id=` | 列表 |
| GET | `/clients/:id` | 详情 |
| PUT | `/clients/:id` | 更新 |
| DELETE | `/clients/:id` | 删除 |
| POST | `/clients/:id/toggle` | body `{enabled}` |
| POST | `/clients/:id/reset` | 清零已用流量 |
| POST | `/clients/:id/token` | 轮换订阅 token |
| POST | `/clients/:id/kick` | 断开该用户全部连接 |
| GET | `/clients/:id/link` | 分享链接与订阅路径 |
| GET | `/clients/:id/usage?days=30` | 每日流量（`days` 取 1–365） |

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

- `psk` 留空自动生成；传统 AEAD 方法接受任意非空密码，SS2022 要求 base64 且长度匹配。
- `quota_bytes=0` 不限；`expires_at=0` 永不过期；`ip_limit=0` 不限。
- `reset_policy` 为 `none` 或 `monthly`；`reset_day` 为 `0`（跟随全局）或 `1–28`。

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

## 审计日志

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/audit` | 查询审计记录（需要会话） |

查询参数：`limit`（默认 100，最大 500）、`offset`、`action`、`username`、`since`/`until`（Unix 秒，`since` 含、`until` 不含）。

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
| `auth.login` / `auth.login.failed` / `auth.logout` | 登录成功、失败（含尝试的用户名）与退出 |
| `admin.password` | 修改管理员密码 |
| `inbound.create` / `.update` / `.delete` / `.toggle` / `.restart` | 入站变更 |
| `client.create` / `.update` / `.delete` / `.toggle` / `.reset` / `.token` / `.kick` | 用户变更 |
| `client.import` / `client.export` | 导入导出 |
| `settings.update` | 系统设置变更 |

- 只记录**成功**的操作；失败的登录例外（记 `auth.login.failed`）。
- `detail` 为固定的 `key=value` 摘要，**不包含**密码、用户 PSK、服务器 PSK、订阅 token。
- 保留策略：90 天，且最多 5 万条（超出按时间删除最旧记录）。

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
  "timezone": "Asia/Shanghai"
}
```

- `public_host` / `public_port` 用于生成分享链接；留空/0 时回退到入站监听地址与端口。
- `allow_private_targets` 立即生效；云元数据地址始终拒绝。
- `timezone` 必须是 IANA 时区名或 `Local`。

## 备份

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/backup` | 下载 `sspanel-YYYYMMDD-HHMMSS.db` 快照 |

恢复不支持在线进行，需要停机替换数据库文件，见 [deployment.md](deployment.md)。

## 订阅

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/sub/:token` | 无 Cookie；返回 base64 列表或 Clash YAML |

- UA 含 `clash`/`meta`/`mihomo`，或查询参数 `format=clash` 时返回 YAML。
- 用户被禁用、已到期、超配额，或订阅功能关闭时返回 `404`。
- 响应头包含 `Profile-Update-Interval: 12`。

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
