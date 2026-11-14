# 部署指南

sspanel 是一个单二进制程序：Web 面板、Shadowsocks 内核和 SQLite 都在同一个进程内。部署时只需要放置二进制、配置文件和数据库目录。

## 1. 环境要求

- Linux x86_64 / arm64（macOS 也可用于开发）。
- 无需 Node、MySQL、Redis、Xray。
- 服务器需要 NTP：Shadowsocks 2022 握手允许的时钟偏差为 ±30 秒。
- 面板端口与 Shadowsocks 入站端口需要在防火墙/安全组放行（面板建议不直接对公网开放）。
- 入站支持 IPv4 与 IPv6 字面量监听地址；`::` 为 IPv6 双栈。使用 IPv6 时，防火墙需分别放行 IPv6 的 TCP/UDP，Docker 部署还需为容器启用 IPv6 或在 `ports` 中显式映射。

## 2. 安装方式

### 2.1 二进制 + systemd（推荐）

在开发机构建 Linux 静态二进制：

```bash
make dist
scp dist/sspanel-linux-amd64 root@server:/tmp/sspanel
scp -r deploy root@server:/tmp/sspanel-deploy
```

在服务器上：

```bash
sudo cp /tmp/sspanel /usr/local/bin/sspanel
sudo chmod 0755 /usr/local/bin/sspanel

# install.sh 需要同目录下的 sspanel.service，所以从复制过去的 deploy/ 里执行
cd /tmp/sspanel-deploy
sudo ./install.sh /usr/local/bin/sspanel
```

`install.sh` 只依赖 `deploy/` 目录里的 `sspanel.service`，不依赖仓库其余部分。

`install.sh` 会：

1. 创建系统用户 `sspanel`（nologin）。
2. 安装二进制到 `/usr/local/bin/sspanel`。
3. 创建 `/etc/sspanel/config.yaml`（已存在则保留）和 `/var/lib/sspanel`，权限分别为 `0600` 和 `0700`。
4. 安装并启动 `sspanel.service`，随后做一次 `/healthz` 检查。

查看初始管理员密码：

```bash
sudo journalctl -u sspanel | grep -A5 'administrator created'
```

日志里只会出现一次随机密码；首次登录后必须修改。

常用命令：

```bash
sudo systemctl status sspanel
sudo systemctl restart sspanel
sudo journalctl -u sspanel -f
```

### 2.2 Docker

```bash
cd deploy
# 首次启动前可在 docker-compose.yml 中设置 SSPANEL_ADMIN_PASSWORD
docker compose up -d --build
docker compose logs -f
```

注意：

- 容器内的配置是 `deploy/config.docker.yaml`，面板监听 `0.0.0.0:2096`。
- compose 文件默认把面板端口映射到宿主机 `127.0.0.1:2096`。
- 在面板中创建的 Shadowsocks 入站端口，需要同步在 `docker-compose.yml` 的 `ports` 中追加 TCP 与 UDP 映射，然后 `docker compose up -d`。
- 数据保存在 `sspanel-data` 卷中。

### 2.3 直接运行（调试）

```bash
cp config.example.yaml config.yaml
./bin/sspanel run --config ./config.yaml
```

## 3. 访问面板

面板默认只监听 `127.0.0.1:2096`，这是刻意的安全默认。

### 3.1 SSH 隧道（最简单）

```bash
ssh -L 2096:127.0.0.1:2096 user@your-server
```

本机浏览器打开 `http://127.0.0.1:2096/`。

### 3.2 TLS 反向代理

Caddy（自动 HTTPS）：

```bash
sudo cp deploy/Caddyfile /etc/caddy/Caddyfile
# 修改 panel.example.com 为你的域名
sudo systemctl reload caddy
```

Nginx：

```bash
sudo cp deploy/nginx.conf /etc/nginx/conf.d/sspanel.conf
# 修改域名与证书路径
sudo nginx -t && sudo systemctl reload nginx
```

反代后如需显示真实客户端 IP（并让登录限流按真实 IP 计数、TLS 反代下正确设置 `Secure` Cookie）：

```yaml
panel:
  listen: 127.0.0.1
  trusted_proxies:
    - 127.0.0.1
```

`trusted_proxies` 默认为空，此时 `X-Forwarded-For` / `X-Forwarded-Proto` 一律忽略。只填写实际的反向代理地址，不要填 `0.0.0.0/0`。

### 3.3 直接启用 HTTPS

```yaml
panel:
  listen: 0.0.0.0
  port: 2096
  tls_cert: /etc/sspanel/fullchain.pem
  tls_key: /etc/sspanel/privkey.pem
```

## 4. 创建第一个节点

1. 登录面板。
2. 「入站管理」→「新建入站」：
   - 监听地址：`0.0.0.0`（IPv6 双栈用 `::`）
   - 端口：例如 `8388`
   - 方法：`2022-blake3-aes-128-gcm`（推荐）或 `2022-blake3-aes-256-gcm`
   - UDP 转发：开启
3. 点击该入站的「用户」→「新建用户」。
4. 在用户列表点击「链接」，复制 `ss://` 链接或订阅地址。

客户端要求：

| 方法 | 可用客户端 |
|---|---|
| `2022-blake3-*` | sing-box、mihomo/Clash.Meta、shadowsocks-rust、Outline 等支持 SS2022 的客户端 |
| `aes-*-gcm`、`chacha20-ietf-poly1305` | 几乎所有 Shadowsocks 客户端 |

订阅地址支持：

- 默认（任意 UA）：base64 编码的 `ss://` 列表。
- UA 含 `clash`、`meta`、`mihomo`：Clash/Mihomo YAML。
- 也可以加 `?format=clash` 强制 YAML。

## 5. 防火墙与端口

只放行必需的端口，例如：

```bash
# Shadowsocks 入站
sudo ufw allow 8388/tcp
sudo ufw allow 8388/udp

# 面板不要对公网开放；如果必须，限制来源 IP
sudo ufw allow from 203.0.113.10 to any port 2096 proto tcp
```

云厂商安全组需要做同样的放行。

## 6. 备份与恢复

### 备份

面板「系统设置」→「下载备份」会通过 `VACUUM INTO` 生成一致的 SQLite 快照，且**不阻塞服务**：快照在一个读事务内完成，备份期间写入照常进行（此前该操作会持写锁约 95 ms / 11.5 MB 库，并随库体积线性增长）。

也可以停机复制：

```bash
sudo systemctl stop sspanel
sudo cp /var/lib/sspanel/sspanel.db /backup/sspanel-$(date +%F).db
sudo systemctl start sspanel
```

### 恢复

```bash
sudo systemctl stop sspanel
sudo cp /backup/sspanel-2026-01-01.db /var/lib/sspanel/sspanel.db
sudo chown sspanel:sspanel /var/lib/sspanel/sspanel.db
sudo systemctl start sspanel
```

备份文件包含管理员密码哈希和全部用户 PSK，请按敏感数据保管。

数据库文件本身由程序在打开时收紧到 `0600`（`-wal` / `-shm` 同），进程 umask 为 `0077`；systemd 单元也设置了 `UMask=0077`。用 `stat -c '%a %n' /var/lib/sspanel/sspanel.db` 可以确认。

## 7. 升级

```bash
sudo systemctl stop sspanel
sudo cp /var/lib/sspanel/sspanel.db /backup/sspanel-pre-upgrade.db
sudo install -m 0755 sspanel-new /usr/local/bin/sspanel
sudo systemctl start sspanel
```

数据库迁移在启动时自动执行，且有事务保护；迁移失败会拒绝启动，不会留下半成品 schema。

升级前建议先跑一次检查，它会指出会导致启动失败的存量数据：

```bash
sudo -u sspanel /usr/local/bin/sspanel check --config /etc/sspanel/config.yaml
```

其中一类是单用户方法（`2022-blake3-chacha20-poly1305`）的入站存在多个启用用户——这种配置无法正确计量，程序会拒绝启动该入站并给出被启用的用户名，请先禁用多余的用户。

`sspanel repair` 用于修复数据面之外的问题：先跑 `PRAGMA integrity_check`，再从明细表重建按天流量汇总。它只应在汇总表与明细不一致时使用，正常写入会事务性地维护两者。

```bash
sudo -u sspanel /usr/local/bin/sspanel repair --config /etc/sspanel/config.yaml
```

## 8. 故障排查

| 现象 | 排查方向 |
|---|---|
| 面板 502 / 无法连接 | `systemctl status sspanel`；确认 `panel.listen` 与反代 upstream 一致 |
| 入站显示已启用但连不上 | 先看面板入站列表是否显示「启动失败」及其 `last_error`（`GET /inbounds` 的 `last_error` 字段）；再检查防火墙；`ss -lntup | grep <port>` |
| SS2022 客户端握手失败 | 服务器时钟偏差 >30s；客户端不支持 SS2022；PSK 长度/编码错误 |
| 面板提示端口占用 | 该端口已有其他程序或另一个入站；`lsof -i:<port>` |
| 流量没有统计 | 计数每 10 秒落库；刷新面板查看；确认客户端实际有数据传输 |
| 数据库 locked | 确认没有同时运行两个实例指向同一个 DB 文件 |
| 内存持续增长 | 检查是否开启大量 UDP 会话；限制用户 IP 数；查看 `sessions` 指标 |

日志位置：systemd 为 `journalctl -u sspanel`，Docker 为 `docker compose logs`。日志中的 `password`、`psk`、`token` 字段已脱敏，`/sub/<token>` 路径不会记录 token。
