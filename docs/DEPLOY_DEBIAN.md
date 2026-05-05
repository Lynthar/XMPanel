# Debian 13 部署 Prosody + XMPanel

一份从零开始把 XMPP 服务器和 Web 管理面板装到一台 VPS 上的实战指南，基于真机部署验证。

> **本文档基于 Debian 13 trixie + Prosody 13.0.5 + Lua 5.4 + PostgreSQL 17 + Go 1.24 + Node 20 实测。** Prosody / mod_http_admin_api 的小版本变化可能影响细节（特别是 `select_role` 那个 bug 是否仍在 —— 见 [`TROUBLESHOOTING.md`](TROUBLESHOOTING.md) 深度专题），如果与本文不符请按排错文档里的"诊断手段"逐步定位。

---

## 目录

- [概览](#概览)
- [先决条件](#先决条件)
- [第一部分 · 系统初始化](#第一部分--系统初始化)
- [第二部分 · 安装 Prosody 13](#第二部分--安装-prosody-13)
- [第三部分 · 安装 XMPanel](#第三部分--安装-xmpanel)
- [第四部分 · 反向代理与 HTTPS](#第四部分--反向代理与-https)
- [第五部分 · 第一次登录与接入 Prosody](#第五部分--第一次登录与接入-prosody)
- [第六部分 · 可选加固](#第六部分--可选加固)
- [附录 A · Prosody 13 admin API 真实端点](#附录-a--prosody-13-admin-api-真实端点)
- [附录 B · 用 ejabberd 替代 Prosody](#附录-b--用-ejabberd-替代-prosody)
- [附录 C · 凭据登记清单](#附录-c--凭据登记清单)

排错另开一份：[`TROUBLESHOOTING.md`](TROUBLESHOOTING.md)。

---

## 怎么读这份文档

按你的目标挑路径：

- **A. 装一台 Prosody + XMPanel 整套**（最常见）
  → 从头读到尾。

- **B. 只想要 Prosody XMPP 服务器，不要 web 面板**
  → 只读 §0 §1（建 prosody 这一个 PG 库即可）§2.1-§2.6 + §2.9。§2.7 之后跟你无关；§3 起跳过。
  > Prosody 自身有 [官方文档](https://prosody.im/doc/) 和 [Debian quickstart](https://prosody.im/doc/debian)，比这份更适合"纯 Prosody 用户"。本文档的 §2 主要价值是把 Debian 13 + Lua 5.4 shebang 修复 + community modules 这条路径写顺。

- **C. Prosody 已经在运行，只想加 XMPanel**
  → 跳到 §2.7 + §2.8 + §2.8.5（patch / token / mod_admin_panel / hosts）→ 然后 §3-§6。

- **遇到问题 / curl 没按预期返回**
  → 先翻 [`TROUBLESHOOTING.md`](TROUBLESHOOTING.md) 按症状对照。

---

## 概览

### 这份文档干嘛的

- 把 XMPP 服务器（Prosody）+ 一个 Web 管理面板（XMPanel）装到**同一台** Debian 13 VPS 上
- 全程带 HTTPS、自动续证、systemd 托管、防火墙、备份
- 解决路上必踩的 ~10 个坑

### 部署完后你能得到什么

- `https://panel.example.com/` 一个浏览器登录的 Web 管理面板，能加 / 删 / 看 XMPP 用户、查审计日志、管理面板自身的管理员账号
- `xmpp.example.com` 一个能用任何 XMPP 客户端（Gajim / Conversations / Dino 等）登录的 XMPP 服务器
- `conference.xmpp.example.com` 一个 MUC（群聊）域

### 拓扑图

```
┌──────────────────────── VPS (Debian 13) ────────────────────────┐
│                                                                  │
│  浏览器 → :443 ─→ nginx ─→ :8080 ─→ XMPanel (Go 二进制)          │
│                                       │                          │
│                                       │ admin API + token        │
│                                       ↓                          │
│                                    :5280 (loopback)              │
│                                       │                          │
│  XMPP 客户端 → :5222 (c2s) ─→ Prosody                            │
│  联邦服务器  → :5269 (s2s)        │                              │
│                                   │ storage                      │
│                                   ↓                              │
│                              PostgreSQL 17                       │
│                              ├─ 库 xmpanel                        │
│                              └─ 库 prosody                        │
└──────────────────────────────────────────────────────────────────┘
```

### 端口表

| 端口 | 协议 | 监听接口 | 用途 |
|---|---|---|---|
| 22（或自定义） | TCP | 0.0.0.0 | SSH（**注意自定义端口的防火墙陷阱**，见 §1.2） |
| 80 | TCP | 0.0.0.0 | HTTP（ACME challenge + nginx 重定向） |
| 443 | TCP | 0.0.0.0 | HTTPS（XMPanel 面板） |
| 5222 | TCP | 0.0.0.0 | XMPP c2s（客户端连服务器） |
| 5269 | TCP | 0.0.0.0 | XMPP s2s（联邦） |
| 5280 | TCP | 127.0.0.1 | Prosody admin API（仅本机，由 XMPanel 调） |
| 5432 | TCP | 127.0.0.1 | PostgreSQL（仅本机） |
| 8080 | TCP | 127.0.0.1 | XMPanel HTTP（仅本机，由 nginx 反代） |

### 全程要保管好的凭据

部署中会生成多个密码 / 密钥 / token，**每生成一个就立刻保存到密码管理器**，本会话结束就找不回来。完整清单见 [附录 C](#附录-c--凭据登记清单)。

---

## 先决条件

- 一台 Debian 13 trixie VPS，最低 1 vCPU / 1 GB RAM / 10 GB 磁盘
- 两个域名（或同一域名的两个子域）：
  - **`xmpp.example.com`** — XMPP 服务用
  - **`panel.example.com`** — Web 面板用
  - 两条都已经 A 记录指到 VPS 的公网 IPv4
  - 还需要 **`conference.xmpp.example.com`** A 记录也指过来（MUC 群聊用，非必需但建议加）
- root 或带 sudo 的用户登录
- 基础 Linux 命令行知识（vim/nano、systemctl、journalctl）

---

## 第一部分 · 系统初始化

### 1.1 软件源、基础工具

```bash
apt update && apt upgrade -y
apt install -y curl wget git build-essential ca-certificates ufw fail2ban
timedatectl set-timezone Asia/Shanghai   # 或 UTC
hostnamectl set-hostname xmpp.example.com
```

### 1.2 防火墙（先开端口再 enable）

> ⚠️ **如果你已经把 SSH 端口改成非 22 的自定义端口**，必须把那个端口也加进 ufw allow，否则 `ufw enable` 会立刻把你锁在外面。下面脚本自动从 sshd_config 读真实端口。

```bash
SSH_PORT=$(awk '/^Port /{print $2}' /etc/ssh/sshd_config)
SSH_PORT=${SSH_PORT:-22}
echo "SSH listens on: $SSH_PORT"

ufw default deny incoming
ufw default allow outgoing
ufw allow ${SSH_PORT}/tcp comment 'SSH'
ufw allow 80/tcp                  # HTTP
ufw allow 443/tcp                 # HTTPS
ufw allow 5222/tcp                # XMPP c2s
ufw allow 5269/tcp                # XMPP s2s
# 5280 / 5281 不用对外开放（仅 loopback）
ufw enable
```

> **被锁外的救援**：登 VPS 服务商面板的 Web Console / VNC，跑 `ufw allow <你的SSH端口>/tcp`，或 `ufw disable` 临时关。

### 1.3 安装 PostgreSQL 17

```bash
apt install -y postgresql postgresql-contrib
systemctl enable --now postgresql
```

为 XMPanel 建一个独立的 PG 用户和库（密码用 `openssl rand` 生成，**立刻保存到密码管理器**）：

```bash
XMPANEL_DB_PASS=$(openssl rand -hex 24)
echo "XMPANEL_DB_PASS=$XMPANEL_DB_PASS"
# ☆ 立刻保存到密码管理器

sudo -u postgres psql <<SQL
CREATE USER xmpanel WITH PASSWORD '${XMPANEL_DB_PASS}';
CREATE DATABASE xmpanel OWNER xmpanel;
GRANT ALL PRIVILEGES ON DATABASE xmpanel TO xmpanel;
SQL

# 验证
PGPASSWORD="$XMPANEL_DB_PASS" psql -h localhost -U xmpanel -d xmpanel -c '\conninfo'
```

> 用 `openssl rand -hex 24` 而不是 `-base64` 是因为 hex 输出只含 `0-9a-f`，**避免 yaml 字符串里出现需要转义的 `+ / =`**。

---

## 第二部分 · 安装 Prosody 13

### 2.1 装 Prosody + Lua 5.4

Debian trixie 默认仓库的 Prosody 偏旧，用 prosody.im 上游仓库装 13.0.5：

```bash
apt install -y extrepo
extrepo enable prosody
apt update

# Prosody 13 不兼容 Lua 5.1，必须明确装 5.4
apt install -y prosody \
    lua5.4 liblua5.4-dev \
    lua-sec lua-dbi-postgresql lua-expat lua-filesystem lua-socket \
    lua-unbound lua-readline lua-zlib
```

#### ⚠️ 修复 prosody 二进制的 shebang

`/usr/bin/prosody` 是 Lua 脚本（`ldd` 输出 `not a dynamic executable`），shebang 默认 `#!/usr/bin/env lua` → 解析到 lua5.1 → 启动失败报 `Prosody is no longer compatible with Lua 5.1`。**必须手动改 shebang**：

```bash
update-alternatives --set lua-interpreter /usr/bin/lua5.4
sed -i '1c#!/usr/bin/lua5.4' /usr/bin/prosody
sed -i '1c#!/usr/bin/lua5.4' /usr/bin/prosodyctl
head -1 /usr/bin/prosody       # 应输出 #!/usr/bin/lua5.4
```

### 2.2 拉 Community modules

```bash
apt install -y mercurial
hg clone https://hg.prosody.im/prosody-modules/ /opt/prosody-modules
chown -R root:prosody /opt/prosody-modules
chmod -R g+rX /opt/prosody-modules
```

`mod_http_admin_api`、`mod_http_oauth2` 都在 community modules 里。

### 2.3 为 Prosody 创建独立 PG 用户/库

```bash
PROSODY_DB_PASS=$(openssl rand -hex 24)
echo "PROSODY_DB_PASS=$PROSODY_DB_PASS"
# ☆ 立刻保存到密码管理器

sudo -u postgres psql <<SQL
CREATE USER prosody WITH PASSWORD '${PROSODY_DB_PASS}';
CREATE DATABASE prosody OWNER prosody;
SQL

# 验证
PGPASSWORD="$PROSODY_DB_PASS" psql -h localhost -U prosody -d prosody -c '\conninfo'
```

### 2.4 写 `/etc/prosody/prosody.cfg.lua`

> ⚠️ **复制粘贴 4 个常见错**（每条都在实测中踩过）：
> 1. 不要把 Markdown 围栏 ` ```lua` 一起复制进去 → Prosody 会在第 1 行报 `unexpected symbol near '\`'`
> 2. heredoc 整段必须**严格顶格**（结束符 `LUA_EOF` 前一个字符都不能有），否则 bash 一直等输入
> 3. 不要在 SSH 客户端里用 `\` 续行 → 部分客户端会插入 NBSP 等不可见字符
> 4. 用 sed 替换占位符前用 `echo "$VAR"` 确认变量非空，否则会把占位符替成空字符串

把 `xmpp.example.com` 改成你的真实域名再粘贴：

```bash
cat > /etc/prosody/prosody.cfg.lua <<'LUA_EOF'
admins = { "admin@xmpp.example.com" }

plugin_paths = { "/opt/prosody-modules", "/etc/prosody/modules" }
http_default_host = "xmpp.example.com"

modules_enabled = {
    -- 基础
    "roster"; "saslauth"; "tls"; "dialback"; "disco";
    "carbons"; "pep"; "private"; "blocklist"; "vcard4"; "vcard_legacy";
    "version"; "uptime"; "time"; "ping"; "register"; "mam";
    "smacks"; "csi_simple";
    -- HTTP 管理 API（XMPanel 对接关键）
    "http"; "http_admin_api"; "tokenauth"; "http_oauth2"; "admin_shell";
    -- 反垃圾 / 限速
    "limits";
}

modules_disabled = { "s2s" }   -- 不需要联邦就禁用

storage = "sql"
sql = {
    driver = "PostgreSQL";
    database = "prosody";
    username = "prosody";
    password = "CHANGE_ME_PROSODY_DB_PASSWORD";  -- 下一步用 sed 替换
    host = "localhost";
}

authentication = "internal_hashed"
allow_registration = false
c2s_require_encryption = true
s2s_require_encryption = true
s2s_secure_auth = false

pidfile = "/run/prosody/prosody.pid"
log = {
    info  = "/var/log/prosody/prosody.log";
    error = "/var/log/prosody/prosody.err";
}

-- HTTP admin API 仅监听本地，由 XMPanel 直接调（不走 nginx）
http_ports = { 5280 }
http_interfaces = { "127.0.0.1" }
https_ports = {}
trusted_proxies = { "127.0.0.1", "::1" }

-- mod_http_oauth2：放宽 token TTL，方便后端集成
allowed_oauth2_grant_types = { "authorization_code"; "device_code"; "password"; }
oauth2_access_token_ttl = 2592000      -- 30 天
oauth2_refresh_token_ttl = 31536000    -- 1 年
allow_unencrypted_oauth2 = true        -- 只本机 loopback 时需要

VirtualHost "xmpp.example.com"
    -- 注意：http_admin_api / http_oauth2 必须在这个 host 块里再写一遍才会注册路由
    modules_enabled = { "http_admin_api"; "http_oauth2" }
    -- ssl 块在 §2.6 申完证书再加

Component "conference.xmpp.example.com" "muc"
    modules_enabled = { "muc_mam" }
    restrict_room_creation = "local"
LUA_EOF
```

把 PG 密码替换进去：

```bash
sed -i -E "s|password = \"CHANGE_ME_PROSODY_DB_PASSWORD\";|password = \"${PROSODY_DB_PASS}\";|" /etc/prosody/prosody.cfg.lua

# 检查替换成功
grep -E '(CHANGE_ME|REPLACE_WITH)' /etc/prosody/prosody.cfg.lua && echo "❌ 还有占位符" || echo "✅ 占位符全部替换"
```

校验语法 + 启动：

```bash
prosodyctl check config       # 应输出 "All checks passed, congratulations!"
systemctl enable --now prosody
systemctl status prosody --no-pager
```

> **配置项黑名单**（这两个项**不存在**，看到任何老教程里写它们都是错的）：
> - `http_admin_api_token = "..."` — Prosody 13 mod_http_admin_api 没有静态 API key
> - `http_admin_api_auth = "token"` — 同上
>
> Prosody 13 的 admin API 鉴权**只**走 mod_tokenauth Bearer token，签发流程见 §2.7。

### 2.5 创建 admin XMPP 账号

```bash
prosodyctl register admin xmpp.example.com
# 交互式输入两次密码 → ☆ 保存到密码管理器
```

> 用 `register` 而不是 `adduser` —— 后者要求 mod_admin_shell 在线，前者是离线工具。`register` 子命令的参数是 `username host`，不是 JID。

### 2.6 申请 TLS 证书 + 加 SSL 块

```bash
apt install -y certbot
systemctl stop prosody       # certbot --standalone 临时占 80 端口

certbot certonly --standalone \
  -d xmpp.example.com \
  -d conference.xmpp.example.com \
  --agree-tos -m 你的邮箱@example.com -n

# 让 prosody 用户能读证书
prosodyctl --root cert import /etc/letsencrypt/live
chgrp -R prosody /etc/letsencrypt/live /etc/letsencrypt/archive
chmod -R g+rX /etc/letsencrypt/live /etc/letsencrypt/archive

# 验证证书覆盖了两个域名（一张 SAN 证书够用，prosodyctl 只导出主域那对 .crt/.key 是正常的）
openssl x509 -in /etc/prosody/certs/xmpp.example.com.crt -noout -text | grep -A1 "Subject Alternative Name"
```

把 SSL 块插进 VirtualHost：

```bash
python3 - <<'PY'
import re
DOMAIN = "xmpp.example.com"   # ← 改成你的
path = "/etc/prosody/prosody.cfg.lua"
with open(path) as f:
    content = f.read()
ssl_block = f'''    ssl = {{
        certificate = "/etc/prosody/certs/{DOMAIN}.crt";
        key         = "/etc/prosody/certs/{DOMAIN}.key";
    }}
'''
new = re.sub(
    rf'(VirtualHost "{re.escape(DOMAIN)}"\n)(?!    ssl)',
    rf'\1{ssl_block}',
    content,
    count=1
)
with open(path, "w") as f:
    f.write(new)
PY

grep -A 6 '^VirtualHost' /etc/prosody/prosody.cfg.lua
systemctl start prosody
prosodyctl check config
```

### 2.7 给 XMPanel 签 admin Bearer token

> **本节是 Prosody 13 部署最容易卡住的地方**，请逐步执行。背景见 [`TROUBLESHOOTING.md`](TROUBLESHOOTING.md) 深度专题。

#### 步骤 1：patch mod_tokenauth.lua（绕过一个静默 401 bug）

如果不打 patch，签出来的 token 调 admin API 会无声地返回 401（且 prosody 日志里**没有任何错误信息**）。详情见排错文档。

```bash
python3 - <<'PY'
import re
path = "/usr/lib/prosody/modules/mod_tokenauth.lua"
with open(path) as f:
    c = f.read()
pattern = re.compile(
    r'local function select_role\(username, host, role_name\).*?\nend',
    re.DOTALL
)
new = '''local function select_role(username, host, role_name)
    if not role_name then return end
    local role = usermanager.get_role_by_name(role_name, host)
    if not role then return end
    return role
end'''
c = pattern.sub(new, c, count=1)
with open(path, 'w') as f:
    f.write(c)
PY

# 必须验证语法
luac5.4 -p /usr/lib/prosody/modules/mod_tokenauth.lua && echo "✅ syntax OK"

# 验证 patch 内容（应为 5 行干净版本）
sed -n '/^local function select_role/,/^end$/p' /usr/lib/prosody/modules/mod_tokenauth.lua
```

> **这个 patch 简化了 token role 校验**：跳过了"用户是否被允许穿戴这个 role"的二次检查。在 server-to-server 后端集成场景下安全（token 由 admin 签发，secret 不可伪造）。完整安全分析见 [`TROUBLESHOOTING.md`](TROUBLESHOOTING.md)。

防止 apt 升级 prosody 包时把 patch 覆盖掉：

```bash
mkdir -p /etc/prosody/patches
cp /usr/lib/prosody/modules/mod_tokenauth.lua /etc/prosody/patches/mod_tokenauth.lua

cat > /etc/apt/apt.conf.d/99-prosody-patch <<'CONF'
DPkg::Post-Invoke {
    "if [ -f /etc/prosody/patches/mod_tokenauth.lua ] && [ -f /usr/lib/prosody/modules/mod_tokenauth.lua ]; then cmp -s /etc/prosody/patches/mod_tokenauth.lua /usr/lib/prosody/modules/mod_tokenauth.lua || cp /etc/prosody/patches/mod_tokenauth.lua /usr/lib/prosody/modules/mod_tokenauth.lua; fi";
};
CONF
```

#### 步骤 2：写一个临时模块用 mod_tokenauth Lua API 签 token

```bash
mkdir -p /etc/prosody/modules
cat > /etc/prosody/modules/mod_admin_token_helper.lua <<'LUA'
local tokenauth = module:depends("tokenauth")
module:hook("module-loaded", function()
    local jid_admin = "admin@" .. module.host
    local grant = tokenauth.create_grant(jid_admin, jid_admin, nil, {role = "prosody:admin"})
    if not grant then return end
    local token = tokenauth.create_token(jid_admin, grant, "prosody:admin", 2592000, nil, nil)
    if not token then return end
    local f = io.open("/tmp/prosody_admin_token.txt", "w")
    f:write(token)
    f:close()
    module:log("warn", "Admin token written to /tmp/prosody_admin_token.txt (grant=%s)", grant.id)
end)
LUA
```

把临时 mod 挂到 VirtualHost：

```bash
sed -i -E 's|(modules_enabled = \{ "http_admin_api"; "http_oauth2";? \})|modules_enabled = { "http_admin_api"; "http_oauth2"; "admin_token_helper" }|' /etc/prosody/prosody.cfg.lua

# 验证替换
grep -A 1 'VirtualHost "xmpp' /etc/prosody/prosody.cfg.lua

systemctl restart prosody
sleep 2
```

#### 步骤 3：取出 token + 测试 admin API

```bash
TOKEN=$(cat /tmp/prosody_admin_token.txt)
echo "TOKEN=$TOKEN"
# ☆ 保存到密码管理器（30 天有效期）

curl -i -H "Host: xmpp.example.com" -H "Authorization: Bearer $TOKEN" \
  http://127.0.0.1:5280/admin_api/server/info
```

期望返回：

```
HTTP/1.1 200 OK
Content-Type: application/json

{"site_name":"xmpp.example.com","version":"13.0.5"}
```

如果是 401 / 404 / 500，**先看 [`TROUBLESHOOTING.md`](TROUBLESHOOTING.md) 对照症状**。

#### 步骤 4：撤掉临时模块 + 备份 token

每次重启 prosody，临时 mod 都会**覆盖** /tmp/prosody_admin_token.txt 签新 token，运维上很乱。备份后撤掉：

```bash
# 1. 备份 token 到 root 家目录
cp /tmp/prosody_admin_token.txt /root/xmpanel-prosody-token.txt
chmod 600 /root/xmpanel-prosody-token.txt

# 2. 撤掉临时模块
sed -i 's|; "admin_token_helper"||' /etc/prosody/prosody.cfg.lua

# 3. 重启使配置生效
systemctl restart prosody

# 4. 用备份 token 再测一次（仍应 200）
TOKEN=$(cat /root/xmpanel-prosody-token.txt)
curl -i -H "Host: xmpp.example.com" -H "Authorization: Bearer $TOKEN" \
  http://127.0.0.1:5280/admin_api/server/info
```

> **token 30 天后过期怎么办**：把 `mod_admin_token_helper.lua` 重新挂回 `modules_enabled`，`systemctl restart prosody`，备份新 token，撤掉模块。或把 `mod_admin_token_helper.lua` 里 `2592000` 改成 `31536000`（1 年）后重新签。

### 2.8 让 XMPP 域名在本机解析到 127.0.0.1

XMPanel 跟 Prosody 同机器时，TCP 走 loopback 最快。但 Prosody 按 HTTP `Host:` 头路由 admin_api，**Host 头是 `127.0.0.1` 时返回 404**。解决：把 XMPP 域名在本机映射到 127.0.0.1，让 XMPanel 配置时填 `xmpp.example.com`，TCP 走 loopback、HTTP Host 头自然对。

```bash
# 立即生效
echo "127.0.0.1  xmpp.example.com" >> /etc/hosts
getent hosts xmpp.example.com   # 应输出 127.0.0.1
```

#### ⚠️ Debian cloud image：持久化

Debian cloud image 默认 cloud-init 接管 `/etc/hosts`，重启时会**重写**这个文件，手动加的条目会丢。两种持久化方式（任选一个）：

**方式 A**（推荐，更兼容）：把映射加进 cloud-init 的 hosts 模板：

```bash
echo "127.0.0.1  xmpp.example.com" >> /etc/cloud/templates/hosts.debian.tmpl
```

**方式 B**：彻底关掉 cloud-init 管 /etc/hosts：

```bash
sed -i 's/^manage_etc_hosts:.*$/manage_etc_hosts: false/' /etc/cloud/cloud.cfg
grep -q '^manage_etc_hosts' /etc/cloud/cloud.cfg || \
  echo 'manage_etc_hosts: false' >> /etc/cloud/cloud.cfg
```

> **维护时记住**（建议把这段记到 `/root/NOTES.md`）：用了方式 A，将来改 XMPP 域名 → 必须**同步**改 `/etc/cloud/templates/hosts.debian.tmpl`。废弃 XMPanel → 把这条从模板里删掉。

### 2.8.5 安装 mod_admin_panel（让 panel 能管用户 + 控会话）

Prosody 13 的 `mod_http_admin_api` 有两大空白：
1. **它的 PUT /admin_api/users/{name} 实测下没有真正创建用户** —— 返回 200，但 PG 的 accounts 表里没新行。
2. **它根本不暴露在线 c2s session 列表** —— XMPanel 的 Sessions tab 没办法工作。

XMPanel 仓库提供 `prosody/mod_admin_panel.lua` 填这两个空（直接调 prosody 内部 `usermanager.create_user` / `prosody.full_sessions` 等 API），暴露 7 个 REST endpoint：

```
GET    /admin_panel/users
PUT    /admin_panel/users/{username}              -- 真创建用户
DELETE /admin_panel/users/{username}              -- 删用户 + 清 roster/pubsub
PATCH  /admin_panel/users/{username}              -- 改密码

GET    /admin_panel/sessions                      -- 列在线 session
DELETE /admin_panel/sessions/{full_jid}           -- 强制断开一条 session
POST   /admin_panel/sessions/disconnect/{user}    -- 强制断开该用户全部 session
```

```bash
# 1. 把模块 copy 到 prosody 的 plugin 目录
cp /opt/xmpanel/app/prosody/mod_admin_panel.lua /etc/prosody/modules/

# 2. 把它挂到 VirtualHost
sed -i -E 's|(modules_enabled = \{ "http_admin_api"; "http_oauth2";? \})|modules_enabled = { "http_admin_api"; "http_oauth2"; "admin_panel" }|' /etc/prosody/prosody.cfg.lua
grep -A 1 'VirtualHost "xmpp' /etc/prosody/prosody.cfg.lua

# 3. 重启 + 校验
systemctl restart prosody
prosodyctl check config

# 4. 测 endpoint
TOKEN=$(cat /root/xmpanel-prosody-token.txt)
curl -s -H "Host: xmpp.example.com" -H "Authorization: Bearer $TOKEN" \
  http://127.0.0.1:5280/admin_panel/users | python3 -m json.tool
# 期望返回 JSON 数组，至少含 admin

curl -s -H "Host: xmpp.example.com" -H "Authorization: Bearer $TOKEN" \
  http://127.0.0.1:5280/admin_panel/sessions | python3 -m json.tool
# 期望返回 JSON 数组（无在线用户时为空 []）
```

> 这是 XMPanel 项目自己写的 Lua 模块，**不是** Prosody 上游或 community modules 的一部分。鉴权复用 mod_tokenauth Bearer token，跟 mod_http_admin_api 共用同一套 `prosody:admin` 角色判定。
>
> 这一步要求 XMPanel 仓库已经 clone 到 `/opt/xmpanel/app`。如果还没，可以先做完 §3.2 再回来跑。

### 2.9 验收

```bash
TOKEN=$(cat /root/xmpanel-prosody-token.txt)

# A. 服务器信息
curl -s -H "Host: xmpp.example.com" -H "Authorization: Bearer $TOKEN" \
  http://127.0.0.1:5280/admin_api/server/info | python3 -m json.tool

# B. 用户列表（来自 mod_admin_panel —— 它走 usermanager.users 是真的）
curl -s -H "Host: xmpp.example.com" -H "Authorization: Bearer $TOKEN" \
  http://127.0.0.1:5280/admin_panel/users | python3 -m json.tool

# C. 在线 session 列表
curl -s -H "Host: xmpp.example.com" -H "Authorization: Bearer $TOKEN" \
  http://127.0.0.1:5280/admin_panel/sessions | python3 -m json.tool
```

期望：A 返回 `{"site_name": ..., "version": "13.0.5"}`；B 返回数组含 admin 这条；C 返回数组（无在线用户时为空）。

> Prosody 13 的 mod_http_admin_api **不支持** MUC 房间 / 模块管理两类端点（详见[附录 A](#附录-a--prosody-13-admin-api-真实端点)），且它的 user 创建端点实测下不持久化数据，因此 XMPanel 一律走 §2.8.5 的 `mod_admin_panel`。MUC / 模块管理在 XMPanel UI 里通过 capabilities 机制隐藏对应 tab。

---

## 第三部分 · 安装 XMPanel

### 3.1 装 Go + Node 工具链

```bash
# Go 1.24
cd /tmp
wget https://go.dev/dl/go1.24.7.linux-amd64.tar.gz
rm -rf /usr/local/go && tar -C /usr/local -xzf go1.24.7.linux-amd64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' > /etc/profile.d/go.sh
source /etc/profile.d/go.sh
go version    # 应输出 go version go1.24.7 linux/amd64

# Node 20
curl -fsSL https://deb.nodesource.com/setup_20.x | bash -
apt install -y nodejs
node -v && npm -v
```

### 3.2 创建用户、clone、编译

```bash
useradd -r -m -d /opt/xmpanel -s /bin/bash xmpanel || true

cd /opt/xmpanel
sudo -u xmpanel git clone https://github.com/Lynthar/XMPanel.git app   # 改成你的 fork
cd app

# 让 root 也能跑 git 命令（仓库归 xmpanel 用户）
git config --global --add safe.directory /opt/xmpanel/app

# 装依赖
sudo -u xmpanel /usr/local/go/bin/go mod tidy
sudo -u xmpanel npm --prefix web install

# 编译（让 sudo 继承 PATH 含 /usr/local/go/bin）
export PATH=/usr/local/go/bin:$PATH
sudo -u xmpanel env "PATH=$PATH" go build -o xmpanel ./cmd/server
sudo -u xmpanel env "PATH=$PATH" npm --prefix web run build

# 验证产物
ls -la xmpanel web/dist/index.html
```

> 如果 vite 报 `ENOTEMPTY: prepareOutDir` 错（macOS NFS 偶发，普通 Debian 一般不会），`rm -rf web/dist` 重跑一次即可。

### 3.3 写 config.yaml

生成需要的密钥：

```bash
JWT_SECRET=$(openssl rand -base64 48)
DB_ENC_KEY=$(openssl rand -base64 32)
echo "JWT_SECRET=$JWT_SECRET"
echo "DB_ENC_KEY=$DB_ENC_KEY"
echo "XMPANEL_DB_PASS=$XMPANEL_DB_PASS"   # §1.3 已生成；如果会话丢了，回头看密码管理器
# ☆ 三个都保存到密码管理器
```

> 如果 `$XMPANEL_DB_PASS` 在当前 shell 里是空的（之前会话丢了），用密码管理器里那个值；或者 `sudo -u postgres psql -c "ALTER USER xmpanel WITH PASSWORD '新密码';"` 重置。

写配置：

```bash
cat > /opt/xmpanel/app/config.yaml <<YAML_EOF
server:
  address: "127.0.0.1:8080"
  tls:
    enabled: false              # TLS 由 nginx 终结

database:
  driver: "postgres"
  dsn: "host=localhost port=5432 user=xmpanel password=${XMPANEL_DB_PASS} dbname=xmpanel sslmode=disable"
  encryption_key: "${DB_ENC_KEY}"
  max_open_conns: 25
  max_idle_conns: 5
  conn_max_lifetime: "5m"

security:
  jwt:
    secret: "${JWT_SECRET}"
    access_token_ttl: 15m
    refresh_token_ttl: 168h
    issuer: "xmpanel"
  mfa:
    enabled: true
    issuer: "XMPanel"
    required: false
  password:
    min_length: 12
    require_upper: true
    require_lower: true
    require_number: true
    require_special: false
    argon2_time: 3
    argon2_memory: 65536
    argon2_threads: 4
    max_login_attempts: 5
    lockout_duration: 15m
  rate_limit:
    enabled: true
    requests_per_second: 100
    burst: 200
    login_attempts: 5
    login_window: 15m
    trust_x_forwarded_for: true
    trusted_proxies:
      - "127.0.0.1/32"
      - "::1/128"
  cors:
    allowed_origins:
      - "https://panel.example.com"   # ← 改成你的 panel 域名
    allow_credentials: true
    max_age: 86400
  cookies:
    # nginx 在前面终结 TLS、XMPanel 自己跑 HTTP loopback —— 必须显式 always，
    # 否则 cookies 无 Secure 标记。如果你直接 HTTPS 暴露 XMPanel（没有 nginx）
    # 改成 "auto"；本地开发直接 http://127.0.0.1:8080 用 "never"。
    secure_override: "always"

xmpp:
  servers: []                   # 通过 Web UI 添加
YAML_EOF

# 收紧权限
chown xmpanel:xmpanel /opt/xmpanel/app/config.yaml
chmod 600 /opt/xmpanel/app/config.yaml
```

### 3.4 systemd unit

```bash
cat > /etc/systemd/system/xmpanel.service <<'UNIT_EOF'
[Unit]
Description=XMPanel - XMPP admin panel
After=network.target postgresql.service prosody.service
Requires=postgresql.service

[Service]
Type=simple
User=xmpanel
Group=xmpanel
WorkingDirectory=/opt/xmpanel/app
ExecStart=/opt/xmpanel/app/xmpanel
Environment=XMPANEL_CONFIG=/opt/xmpanel/app/config.yaml
Restart=on-failure
RestartSec=5

# 安全沙箱
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/opt/xmpanel/app
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictNamespaces=true
RestrictRealtime=true
LockPersonality=true
SystemCallArchitectures=native

[Install]
WantedBy=multi-user.target
UNIT_EOF

systemctl daemon-reload
systemctl enable --now xmpanel
sleep 3
systemctl status xmpanel --no-pager | head -10
```

### 3.5 抓初始 admin 密码 ⚠️

XMPanel 启动时如果 `users` 表为空，会**自动**创建一个 admin 账号，密码是随机 16 字符，**只在 systemd 日志里出现一次**。

```bash
journalctl -u xmpanel --no-pager | grep -B 1 -A 3 "INITIAL ADMIN"
```

期望输出：

```
INITIAL ADMIN ACCOUNT CREATED
Username: admin
Password: <16 字符随机串>
IMPORTANT: Change this password immediately!
```

☆ **这个密码必须立刻保存到密码管理器**。

> **如果 grep 没找到**：可能日志被旋转，或服务卡在 init 阶段失败导致没跑到创建步骤。先 `journalctl -u xmpanel --no-pager -n 100` 看完整启动日志找原因。如果实在找不回来，跑 `--reset-admin` 重建（详见 [`TROUBLESHOOTING.md`](TROUBLESHOOTING.md)）。

健康检查：

```bash
curl -i http://127.0.0.1:8080/health
# 期望：HTTP/1.1 200 OK + {"status":"ok"}
```

---

## 第四部分 · 反向代理与 HTTPS

### 4.1 申请 panel.example.com 证书

```bash
apt install -y nginx
systemctl stop nginx 2>/dev/null

certbot certonly --standalone \
  -d panel.example.com \
  --agree-tos -m 你的邮箱@example.com -n

ls -la /etc/letsencrypt/live/panel.example.com/
```

### 4.2 nginx 配置

```bash
cat > /etc/nginx/sites-available/panel.example.com <<'NGX_EOF'
server {
    listen 80;
    listen [::]:80;
    server_name panel.example.com;

    location /.well-known/acme-challenge/ { root /var/www/html; }
    location / { return 301 https://$host$request_uri; }
}

server {
    listen 443 ssl;
    listen [::]:443 ssl;
    http2 on;
    server_name panel.example.com;

    ssl_certificate     /etc/letsencrypt/live/panel.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/panel.example.com/privkey.pem;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_prefer_server_ciphers off;
    ssl_session_cache shared:SSL:10m;
    ssl_session_timeout 1d;

    client_max_body_size 1m;
    proxy_read_timeout 60s;

    # 让 XMPanel 拿到真实客户端 IP（XMPanel config 已 trust 127.0.0.1）
    proxy_set_header Host              $host;
    proxy_set_header X-Real-IP         $remote_addr;
    proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;

    location / {
        proxy_pass http://127.0.0.1:8080;
    }
}
NGX_EOF

ln -sf /etc/nginx/sites-available/panel.example.com /etc/nginx/sites-enabled/
rm -f /etc/nginx/sites-enabled/default

nginx -t && systemctl enable --now nginx
```

> nginx 1.25+ 把 `listen 443 ssl http2;` 标记为 deprecated，要分两行：`listen 443 ssl;` + `http2 on;`。

### 4.3 自动续证 + 续证后让 nginx/prosody 重读证书

certbot 装包时已经带了 systemd timer，自动续期不用配 cron。但要加一个 hook 在续期成功后让 nginx 和 prosody 重读证书：

```bash
mkdir -p /etc/letsencrypt/renewal-hooks/post
cat > /etc/letsencrypt/renewal-hooks/post/reload-services.sh <<'HOOK_EOF'
#!/bin/bash
systemctl reload nginx
prosodyctl --root cert import /etc/letsencrypt/live
systemctl reload prosody
HOOK_EOF
chmod +x /etc/letsencrypt/renewal-hooks/post/reload-services.sh

systemctl status certbot.timer --no-pager | head -5
```

### 4.4 通联测试

```bash
# A. HTTP → HTTPS 重定向
curl -i http://panel.example.com/health

# B. HTTPS 健康检查
curl -i https://panel.example.com/health

# C. 拉首页（应是 XMPanel index.html）
curl -s https://panel.example.com/ | head -20
```

期望 A 返回 301、B 返回 200 + JSON、C 返回含 `<title>XMPanel</title>` 的 HTML。

---

## 第五部分 · 第一次登录与接入 Prosody

### 5.1 浏览器登录

1. 打开 `https://panel.example.com/`
2. 用 §3.5 抓到的初始 admin 密码登录
3. **立刻**进 Settings → Change Password 改密码
4. （强烈建议）启用 MFA（用 Google Authenticator / Authy / 1Password 都行），保存恢复码

### 5.2 添加 Prosody 服务器

进 Servers → Add Server，填：

| 字段 | 值 |
|---|---|
| Name | `Prosody Main` |
| Type | `prosody` |
| Host | **`xmpp.example.com`**（**不是** `127.0.0.1`！靠 §2.8 的 /etc/hosts 解析到 loopback） |
| Port | `5280` |
| API Key | 把 `/root/xmpanel-prosody-token.txt` 的内容（`secret-token:...`）整段贴进来 |
| TLS | 关掉（loopback HTTP，没有 TLS） |

提交后：
1. 在卡片菜单点 **Test Connection** —— 期望 `Connection successful`
2. 点卡片进 **ServerDetail** → Users tab → 应该自动列出 admin 用户

### 5.3 已知能用 / 不能用的功能

| 功能 | 状态 |
|---|---|
| 系统用户管理（XMPanel 自身的管理员/操作员） | ✅ |
| 审计日志 | ✅ |
| Settings（profile / 改密码 / MFA） | ✅ |
| Servers 列表 / 添加 / 删除 / 测试连接 | ✅ |
| ServerDetail → Users tab：列用户 / 创建 / 删除 / CSV 批量导入 / 批量删除 / 用户名筛选 | ✅ |
| ServerDetail → Sessions tab（在线 session 列表 / 强制下线） | ✅ 需要装了 §2.8.5 mod_admin_panel |
| Dashboard 的"在线用户数" / "活跃 sessions" 统计 | ⚠️ 显示 — 表示不可用（mod_http_admin_api 不暴露聚合计数） |
| ServerDetail → Rooms tab | ❌ 不显示（Prosody 13 mod_http_admin_api 不支持 MUC 管理；adapter capabilities 里 rooms=false） |

> 这是 Prosody 13 mod_http_admin_api 当前能力的限制，不是 XMPanel 本身的 bug。详细见[附录 A](#附录-a--prosody-13-admin-api-真实端点)。

---

## 第六部分 · 可选加固

### 6.1 fail2ban（保护 SSH 和 XMPanel 登录）

XMPanel 自身已有 IP+用户名维度的登录限速（`security.password.max_login_attempts` + `lockout_duration`），fail2ban 主要给 SSH 兜底；XMPanel 这一侧目前**没有现成可用的 fail2ban filter**（见下方 ⚠️）。

```bash
cat > /etc/fail2ban/jail.local <<'EOF'
[sshd]
enabled = true
backend = systemd        # Debian 13 默认用 journald 而非 /var/log/auth.log
maxretry = 5
bantime = 1h
EOF

systemctl restart fail2ban
fail2ban-client status sshd
```

#### ⚠️ 给 XMPanel 加 fail2ban 没那么容易

天然想法是把"`auth.login_failed` 事件 → fail2ban 拉黑客户端 IP"，但 XMPanel 的失败登录**只写进 PostgreSQL `audit_logs` 表**（`internal/api/handler/auth.go` 里 `h.audit.LogEvent(...)`），**不写 syslog / journal**。zap logger 只输出内部错误（如 `failed to query user`），里头没有客户端 IP / `reason` 字段，无法用 regex 捞出来。

要让 XMPanel 接上 fail2ban，至少二选一：

1. **加一段失败登录的 stderr 日志**（修代码）：在 `auth.go` 的 `AuditActionLoginFailed` 旁边再调一次 `h.logger.Warn(...)`，把 `client_ip` / `reason` 输出到 stderr，systemd 自动落到 journald → fail2ban 用 `backend = systemd` + `journalmatch = _SYSTEMD_UNIT=xmpanel.service` + `failregex = .*client_ip=<HOST>.*reason=invalid_password.*` 匹配。
2. **写一个 PG → 文件 的转储**（不改代码）：拿 cron 定期把 `audit_logs` 里 `action='auth.login_failed'` 的新行格式化输出到 `/var/log/xmpanel-failed.log`，再让 fail2ban 读这个文件。延迟高，不推荐。

老版本本节给过一份 filter（`failregex = "ip":"<HOST>".*"reason":"invalid_password"` 直接读 `/var/log/syslog`），那段配置**实测在 Debian 13 上不会触发任何匹配**——已从本文删除。等代码里加了显式失败日志再补完整 jail。

### 6.2 备份

```bash
mkdir -p /var/backups && chown postgres:postgres /var/backups

cat > /etc/cron.d/xmpanel-backup <<'EOF'
0 2 * * * postgres pg_dump xmpanel  | gzip > /var/backups/xmpanel-$(date +\%F).sql.gz
0 2 * * * postgres pg_dump prosody  | gzip > /var/backups/prosody-$(date +\%F).sql.gz
EOF

# 日志旋转上限
journalctl --vacuum-size=500M
```

### 6.3 整体验收

```bash
# 服务状态
systemctl is-active prosody xmpanel postgresql nginx

# 端口
ss -tlnp | grep -E ':(443|5222|5269|5280|8080)\b'

# Prosody admin API
TOKEN=$(cat /root/xmpanel-prosody-token.txt)
curl -sf -H "Host: xmpp.example.com" -H "Authorization: Bearer $TOKEN" \
  http://127.0.0.1:5280/admin_api/server/info | python3 -m json.tool

# XMPanel 健康
curl -sf https://panel.example.com/health

# 用 XMPP 客户端实测：Gajim / Conversations / Dino 连 admin@xmpp.example.com
```

---

## 附录 A · Prosody 13 admin API 真实端点

`mod_http_admin_api` 13.0.5 实际只支持以下端点（来源：`/opt/prosody-modules/mod_http_admin_api/openapi.yaml`）：

```
GET    /admin_api/server/info
GET    /admin_api/server/metrics       ← 13.0.5 实测有 500 bug，慎用
POST   /admin_api/server/announcement

GET    /admin_api/users
GET    /admin_api/users/{username}
PUT    /admin_api/users/{username}
PATCH  /admin_api/users/{username}
DELETE /admin_api/users/{username}
GET    /admin_api/users/{username}/groups
GET    /admin_api/users/{username}/debug

GET    /admin_api/groups
POST   /admin_api/groups
GET    /admin_api/groups/{id}
DELETE /admin_api/groups/{id}
PUT    /admin_api/groups/{id}/members/{username}
DELETE /admin_api/groups/{id}/members/{username}

GET    /admin_api/invites
GET    /admin_api/invites/{id}
DELETE /admin_api/invites/{id}
POST   /admin_api/invites/account
POST   /admin_api/invites/group
POST   /admin_api/invites/reset
```

XMPanel 仓库的 `prosody/mod_admin_panel.lua`（§2.8.5）补上**真正的用户 CRUD**和**在线 session 管理**：

```
GET    /admin_panel/users                              list
PUT    /admin_panel/users/{username}                   create  (body: {password})
DELETE /admin_panel/users/{username}                   delete + purge
PATCH  /admin_panel/users/{username}                   set password

GET    /admin_panel/sessions                           list active c2s
DELETE /admin_panel/sessions/{full_jid}                disconnect one session
POST   /admin_panel/sessions/disconnect/{username}     disconnect all of user
```

**为什么不直接用 mod_http_admin_api 的 `/admin_api/users/*`**：实测 13.0.5 的 PUT 端点**返回 200 但不持久化用户**（PG accounts 表无新行），不能信任。我们的模块直接调 `usermanager.create_user / delete_user / set_password / users`，行为可预测。

**Prosody 13 mod_http_admin_api 不支持**：
- MUC 房间管理
- 模块管理（启用 / 禁用）

XMPanel 的 prosody adapter 已经把对应方法改为 `return ErrNotImplemented`，UI 上点这些功能拿到 502 而不是诡异的 404；同时通过 capabilities 端点告知前端隐藏对应 tab。

如果你需要 MUC 管理，目前选项：
1. 改装 ejabberd（admin API 更全，见[附录 B](#附录-b--用-ejabberd-替代-prosody)）
2. 自己写 Prosody 模块暴露需要的端点（参考 §2.8.5 的 mod_admin_panel 模式）

---

## 附录 B · 用 ejabberd 替代 Prosody

如果你不想踩 Prosody 13 admin API 那些坑，ejabberd 的 admin API 更成熟：

### B.1 装 ejabberd（替换第二部分）

```bash
apt install -y ejabberd
```

### B.2 启用 mod_http_api

编辑 `/etc/ejabberd/ejabberd.yml`，加：

```yaml
listen:
  - port: 5280
    ip: "127.0.0.1"
    module: ejabberd_http
    request_handlers:
      /api: mod_http_api

api_permissions:
  "admin access":
    who: admin
    what: "*"

commands_admin_access: configure
```

### B.3 创建 admin 账号

```bash
ejabberdctl register admin xmpp.example.com 'StrongAdminPass'
ejabberdctl restart
```

### B.4 在 XMPanel 接入

Add Server 时 Type 选 `ejabberd`，Port `5280`，API Key 用 ejabberd 的 OAuth token 或 admin 凭证。

> ⚠️ XMPanel 的 ejabberd adapter 也**没有对真实 ejabberd 服务器验证过**（CLAUDE.md 里有警告）。具体路径可能仍需对照 ejabberd 文档微调。

---

## 附录 C · 凭据登记清单

部署中按生成顺序列出所有要保存的凭据。建议在密码管理器里建一个 `XMPanel-VPS` 文件夹，每条独立保存：

| # | 名字 | 生成位置 | 用途 |
|---|---|---|---|
| 1 | XMPanel PG 密码（`XMPANEL_DB_PASS`） | §1.3 | XMPanel 连 PG 库 |
| 2 | Prosody PG 密码（`PROSODY_DB_PASS`） | §2.3 | Prosody 连 PG 库 |
| 3 | admin XMPP 密码 | §2.5 | XMPP 客户端登录 |
| 4 | XMPanel JWT secret（`JWT_SECRET`） | §3.3 | 签 access/refresh token |
| 5 | XMPanel DB 加密密钥（`DB_ENC_KEY`） | §3.3 | 加密存储的 XMPP server API keys |
| 6 | Prosody admin Bearer token | §2.7 步骤 3 | XMPanel 调 Prosody admin API（30 天有效） |
| 7 | XMPanel 初始 admin 密码 | §3.5 | 第一次登录 Web 面板（**只显示一次**） |

每一条都不可恢复（除了 #6，重新签即可），丢了只能重新生成 + 重新配置相关组件。

---

## 全文档维护说明

- **Prosody 升级时**：`apt upgrade prosody` 后 §2.7 步骤 1 那段 dpkg trigger 会自动把 select_role patch 重新刷上。但仍建议手动 `luac5.4 -p /usr/lib/prosody/modules/mod_tokenauth.lua` 验证一次。
- **XMPanel 升级时**：`cd /opt/xmpanel/app && sudo -u xmpanel git pull && sudo -u xmpanel env "PATH=$PATH" go build -o xmpanel ./cmd/server && sudo -u xmpanel env "PATH=$PATH" npm --prefix web run build && systemctl restart xmpanel`。
- **改 XMPP 域名时**：要同步改的地方有 4 处 —— `/etc/prosody/prosody.cfg.lua`、`/etc/cloud/templates/hosts.debian.tmpl`、Let's Encrypt 重新申证、XMPanel UI 里 server 记录的 Host 字段。
- **token 30 天后过期**：见 §2.7 步骤 4 末尾说明。
