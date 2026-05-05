# 排错速查（Debian 13 + Prosody 13 + XMPanel）

部署 / 运维过程中真机踩过的坑，按"故障类型 → 症状 → 根因 → 修法"组织。

主部署文档：[`DEPLOY_DEBIAN.md`](DEPLOY_DEBIAN.md)。本档里出现的 §x.y 引用都指向那份。

---

## 目录

- [按症状速查](#按症状速查)
  - [Prosody 启动失败](#prosody-启动失败)
  - [配置写入 / 复制粘贴问题](#配置写入--复制粘贴问题)
  - [PostgreSQL 连接](#postgresql-连接)
  - [HTTP 路由 / 鉴权（admin API）](#http-路由--鉴权admin-api)
  - [系统 / 网络 / XMPanel 自身](#系统--网络--xmpanel-自身)
- [深度专题：mod_tokenauth select_role bug](#深度专题mod_tokenauth-select_role-bug)

---

## 按症状速查

### Prosody 启动失败

#### 报 "Prosody is no longer compatible with Lua 5.1"

`/usr/bin/prosody` 是 Lua 脚本，shebang 默认 `#!/usr/bin/env lua` → env 解析到 lua5.1。

```bash
sed -i '1c#!/usr/bin/lua5.4' /usr/bin/prosody
sed -i '1c#!/usr/bin/lua5.4' /usr/bin/prosodyctl
update-alternatives --set lua-interpreter /usr/bin/lua5.4
```

只用 `update-alternatives` 不够 —— prosody 二进制的 shebang 不会跟着变。

#### 报 "/etc/prosody/prosody.cfg.lua:1: unexpected symbol near '`'"

你从 Markdown 文档里把围栏 ` ```lua ` 一起复制进配置文件了。`head -1 /etc/prosody/prosody.cfg.lua` 验证，重新 heredoc 写一遍。

#### 报 "<eof> expected near 'end'"

Lua 源文件被 sed 多次改写后语法损坏。**修法**：`apt install --reinstall -y prosody` 还原文件，再重新只 patch 一次。

**预防**：每次 sed 改 prosody 模块前 `cp xxx.lua xxx.lua.bak`，sed 后 `luac5.4 -p xxx.lua` 验证语法。

#### `prosodyctl check config` 报 "option set under conference.xxx that should be in the global section"

某个全局选项（如 `http_default_host`）被 `>>` 追加到文件末尾，正好落在 Component 块下面。删掉重新加到 VirtualHost **之前**：

```bash
sed -i '/^http_default_host/d' /etc/prosody/prosody.cfg.lua
sed -i '/^plugin_paths/a http_default_host = "xmpp.example.com"' /etc/prosody/prosody.cfg.lua
```

### 配置写入 / 复制粘贴问题

#### `cat > file <<'EOF'` 输完 bash 一直在 `>` 等

SSH 客户端给整段加了缩进，结束符 `EOF` 前面带了空格。**heredoc 结束符必须严格顶格**。或用 `<<-EOF` 允许 tab 缩进（不允许空格）。

#### `curl: (3) URL rejected: Malformed input to a URL function`

你用了 `\` 续行，SSH 客户端在反斜杠后插入了 NBSP 等不可见字符。所有 curl/sed **写成单行**，长命令写到 `.sh` 文件再 `bash xxx.sh`。

#### `grep password prosody.cfg.lua` 显示 `password = "";`（空字符串）

`sed -i "s|占位符|${VAR}|"` 时 `$VAR` 是空（变量在另一会话定义、或拼写错），sed 把占位符替成空白且**没报任何错**。每次替换前先 `echo "$VAR"` 确认非空。

### PostgreSQL 连接

#### `prosodyctl register` 报 "Failed to connect to database: no connection to the server"

PG 用户/库还没建，或者配置文件里 password 是空字符串。诊断：

```bash
sudo -u postgres psql -c '\du' | grep prosody         # 用户存在？
sudo -u postgres psql -c '\l' | grep prosody          # 库存在？
PGPASSWORD="$PASS" psql -h localhost -U prosody -d prosody -c '\conninfo'   # 能连吗？
```

#### XMPanel 报 "pq: password authentication failed for user xmpanel"

config.yaml 里 dsn 的密码跟 PG 用户实际密码不一致。重置：

```bash
NEW=$(openssl rand -hex 24)
sudo -u postgres psql -c "ALTER USER xmpanel WITH PASSWORD '${NEW}';"
sed -i -E "s|(password=)[^ ]+( dbname=xmpanel)|\1${NEW}\2|" /opt/xmpanel/app/config.yaml
systemctl restart xmpanel
```

### HTTP 路由 / 鉴权（admin API）

#### `curl /admin_api/...` 返回 404 + "Unknown host: 127.0.0.1"

Prosody 按 HTTP `Host:` 头路由，curl 默认 Host 是 `127.0.0.1`，不在 VirtualHost 列表里。

加 `-H "Host: xmpp.example.com"`，或者全局加 `http_default_host = "xmpp.example.com"`（必须放在 VirtualHost **之前**）。XMPanel 同机部署的最佳方案是 §2.8 那个 /etc/hosts 映射。

#### `curl /admin_api/...` 返回 404 但 `Unknown host` 字样消失

Host 头对了，但 `mod_http_admin_api` 没在那个 host 上加载。检查 VirtualHost 块里**也写了** `modules_enabled = { "http_admin_api"; "http_oauth2" }` —— 全局列表里加是不够的。

#### `curl /admin_api/...` 401 Unauthorized 但 token 看起来对

最大概率是下面[深度专题](#深度专题mod_tokenauth-select_role-bug)里的 `select_role` bug。先按 §2.7 步骤 1 打 patch。

诊断方式：

```bash
# 1. 看 PG 里 admin 的 role
sudo -u postgres psql prosody -c "SELECT * FROM prosody WHERE \"user\"='admin' AND store='account_roles';"

# 2. 让 mod_tokenauth 的所有 debug log 变 warn 级别（默认能看到）
sed -i 's|module:log("debug"|module:log("warn"|g' /usr/lib/prosody/modules/mod_tokenauth.lua
systemctl restart prosody

# 3. 重发请求看日志
WC=$(wc -l < /var/log/prosody/prosody.log)
TOKEN=$(cat /root/xmpanel-prosody-token.txt)
curl -s -o /dev/null -H "Host: xmpp.example.com" -H "Authorization: Bearer $TOKEN" \
  http://127.0.0.1:5280/admin_api/server/info
sleep 1
tail -n +$((WC + 1)) /var/log/prosody/prosody.log
```

如果 mod_tokenauth 啥日志都没出 → 就是 `select_role` 那条静默路径，去打 patch。

#### `/admin_api/server/metrics` 返回 500

mod_http_admin_api 13.0.5 实测下这个端点抛异常，原因未深查。XMPanel 适配器已经绕过它（用 `/server/info` + `/users` 自己拼 stats）。其他端点不受影响。

### 系统 / 网络 / XMPanel 自身

#### `ufw enable` 后 SSH 立刻断开重连不上

sshd 监听非 22 端口，但 ufw 只放行了 22。

**救援**：登 VPS 服务商面板的 Web Console / VNC，跑 `ufw allow <真实SSH端口>/tcp`，或 `ufw disable` 临时关。

**预防**：见 §1.2 的脚本，会自动从 sshd_config 读真实端口。

#### `/etc/hosts` 的映射重启后丢失

Debian cloud image 的 cloud-init 在重启时**重写** /etc/hosts。修法见 §2.8 持久化。

#### `prosodyctl adduser` 报 "Unable to connect to server - is it running? Is mod_admin_shell enabled?"

`adduser` 走 Unix socket 跟运行中的 prosody 对话，要求 mod_admin_shell。改用 `prosodyctl register`（**离线工具**，不依赖 admin_shell，参数是 `username host` 不是 JID）。

#### XMPanel 启动 panic：`parsing "/api/v1POST /auth/logout"`

XMPanel `RouteGroup.Handle` 把 prefix 跟 method-prefixed pattern 拼错的 bug，在 main 分支已修。如果你 fork 自旧版，pull 一下 / cherry-pick `Fix RouteGroup.Handle to honor method-prefixed patterns` 这个 commit。

#### XMPanel 初始 admin 密码忘了

推荐用 `--reset-admin` flag（保留其他用户）：

```bash
systemctl stop xmpanel
sudo -u xmpanel /opt/xmpanel/app/xmpanel --reset-admin 2>&1 | grep -A 4 "ADMIN ACCOUNT RESET"
systemctl start xmpanel
```

输出会包含一行 `Password: ...` —— 立刻保存。**MFA 会被同步禁用，admin 现有 sessions 全部撤销**；其他账号（操作员、查看者等）不受影响。

副作用：admin 的 access token 在剩余 ≤15 分钟 TTL 内仍可用（JWT 服务端无状态）。如果担心被滥用，重置后再 `systemctl restart xmpanel`，配合密码已变 + sessions 已删 → 旧 token 拿来调 `/auth/refresh` 时会因找不到 session 立即被拒。

#### XMPanel 启用 `cookies.secure_override: always` 后无法登录

- **症状**：登录页填用户名密码后仍停留在登录页，浏览器 F12 看不到 `xmpanel_refresh` / `csrf_token` cookie 被保存
- **根因**：`secure_override: always` 强制 cookie 带 `Secure` 属性。浏览器**拒绝在 HTTP 连接上保存或回传 Secure cookie**，所以这条配置只在前端走 HTTPS 时安全
- **正确使用场景**：nginx 在前面终结 TLS、XMPanel 自己跑 HTTP loopback —— 浏览器看到的是 HTTPS，cookie 会被接受；XMPanel 本机 loopback 不发 cookie
- **错误使用场景**：本地开发直接 `http://127.0.0.1:8080` 访问 panel —— 应该用 `secure_override: never` 或 `auto`
- **修法**：改回 `auto` 或 `never`，重启 xmpanel

#### `[xmpanel]` fail2ban jail 永远 0 hits

- **症状**：照着老版部署文档加了 `[xmpanel]` jail + `failregex = "ip":"<HOST>".*"reason":"invalid_password"`，但 `fail2ban-client status xmpanel` 永远是 0 currently failed / 0 banned，即使你手动连续输错密码 20 次
- **根因**：XMPanel 把失败登录写进 PostgreSQL `audit_logs` 表（`internal/api/handler/auth.go` 里 `h.audit.LogEvent(...)`），**不写 syslog / journal**。zap logger 只输出内部错误（如 `failed to query user`），不含 `reason` 字段，filter 抓不到任何东西
- **修法**：参见 [`DEPLOY_DEBIAN.md`](DEPLOY_DEBIAN.md) §6.1 的 ⚠️ 段落 —— 要么改代码加一条 stderr 日志（推荐），要么用 cron 把 PG 失败行转储到文件给 fail2ban 读
- **当前状态**：XMPanel 自身已有 IP+用户名维度的应用层登录限速（`security.password.max_login_attempts` + `lockout_duration`）。在它之上叠 fail2ban 只是网络层兜底，不是必需的

#### `mfa.required: true` 不生效

- **症状**：`config.yaml` 里写了 `security.mfa.required: true`，但创建的新用户登录时**没有强制 MFA 绑定**
- **现状**：这条配置在 XMPanel 当前版本里**未实施**（代码里只读不用）。如果你需要强制 MFA，目前只能社交工程让管理员主动启用
- **影响 `--reset-admin`**：reset 后 `mfa_enabled = false`，admin 用纯密码就能登进去，**`mfa.required` 不会拦住这一步**

---

## 深度专题：mod_tokenauth select_role bug

### 这是什么 bug

在 Debian 13 trixie + Prosody 13.0.5 + community `mod_http_admin_api` 这套组合上，**用 mod_tokenauth API 签出来的合法 token 调 admin_api 静默返回 401**，prosody 日志里**没有任何错误信息**。

公开网络上没有人完整记录过这个症状链 —— 我们的部署可能是已知第一份。

### 症状链（按诊断顺序）

1. 临时 mod 用 `tokenauth.create_grant + create_token` 成功签出 token，文件写出来了
2. `curl http://127.0.0.1:5280/admin_api/server/info -H "Authorization: Bearer <token>"` → **401**
3. 给 mod_tokenauth.lua 所有 debug log 改成 warn 级别 → 重发请求 → **日志一字未出**
4. 给 `get_token_session` 整个函数体加 pcall + dump，发现：
   - `parse_token` 返回成功（token_id / user / host / secret 都对）
   - `_get_validated_token_info` 返回成功（token_info 是合法 table）
   - `select_role` 返回 **nil**（卡这了）
5. 给 `select_role` 内部加 log，最后定位：
   - `usermanager.get_role_by_name` 返回正确的 `prosody:admin` role 对象
   - `usermanager.user_can_assume_role(username, host, role.name)` 返回 **false**
6. mod_authz_internal 的 `user_can_assume_role` 检查 admin 用户的"主要 role"（`account_roles` store 里 `_default` key 的值），实测中这条等于 `prosody:operator` 不等于 `prosody:admin`，secondary roles 又是空 → 判为不允许穿戴

### 根因代码

`/usr/lib/prosody/modules/mod_tokenauth.lua` 中的 `select_role`：

```lua
local function select_role(username, host, role_name)
    if not role_name then return end
    local role = usermanager.get_role_by_name(role_name, host);
    if not role then return end
    if not usermanager.user_can_assume_role(username, host, role.name) then return end  -- ← 这里
    return role;
end
```

**最荒唐**：这个 return 没有 log，导致整个失败链路在用户态完全不可见 —— admin_api 直接返回 401 给 client，server 端日志一字未出。

### 修法

把 `select_role` 改成只检查 role 是否存在，**跳过** user_can_assume_role 二次校验。token 由 admin 通过 mod_tokenauth 签发流程已经认证，secret 是 18 字节随机数无法伪造，绕过这道二次校验在 server-to-server 后端集成场景下安全。

具体命令见 [`DEPLOY_DEBIAN.md`](DEPLOY_DEBIAN.md) §2.7 步骤 1。

### 安全性边界

这个 patch 的安全前提：
1. **token 颁发受信任** —— `tokenauth.create_token` 只能由 server 内部代码调（我们的临时 mod 是 admin 写的）
2. **token role 字段写入受控** —— role 在签发时就由我们的代码确定，攻击者无法修改
3. **token secret 不可伪造** —— 18 字节随机 + sha256 哈希存储

如果将来你把 OAuth2 客户端 self-service 暴露给最终用户（让他们自己 register client + password grant），这个 patch 必须撤掉，换成正确做法（手动给 admin 用户的 secondary roles 设上 `prosody:admin`）。

### 替代方案（未完整验证）

理论上可以**不打 patch**，而是手动给 admin 用户的 PG `account_roles` store 写一个 secondary role：

```bash
prosodyctl shell
# 进入 prompt 后手动输入（必须互动 shell；heredoc 传入会报语法错）：
# > user:set_role("admin@xmpp.example.com", "prosody:admin")
# > quit

# 验证（应有一行 secondary role）
sudo -u postgres psql prosody -c "SELECT * FROM prosody WHERE store='account_roles';"
```

实测中这条命令我们没跑成功（prosody-shell 的 Lua REPL 处理输入有怪异行为），所以**仍然走 patch 方案最稳**。

### 上游反馈

这个 bug **应当上报给 Prosody 上游**：在 `select_role` 静默 return 路径加 log，让管理员能看到为什么 401。issue tracker：[https://issues.prosody.im](https://issues.prosody.im)。
