<div align="center">

<img src="web/public/favicon.svg" alt="XMPanel" width="72">

# XMPanel

[![license](https://img.shields.io/github/license/Lynthar/XMPanel)](LICENSE)
[![go](https://img.shields.io/github/go-mod/go-version/Lynthar/XMPanel)](go.mod)

</div>

XMPP 服务器（Prosody、ejabberd）与 Matrix 服务器（Synapse、Tuwunel，以及任何走客户端规范接入的实现）的自托管 Web 管理面板：账号与会话管理、RBAC、MFA、防篡改审计日志。Go + React。Matrix 这一侧还在扩展。

[English](README.md) | 简体中文

> **施工中。** 还没有发过版，只能从源码构建。Prosody 这一侧已经在真实服务器上部署使用过；
> 三个适配器都在 CI 里对着真实服务器跑 smoke（`smoke/`）。觉得有意思可以先关注，
> 但别指望拿到一个打包好的产品。

它挂在服务器外面，每台单独登记、各自走一个协议中立的适配器，所以可以同时管好几台。
管的是账号、在线会话和房间，而且只提供每台服务器真正支持的操作：登记时面板会探测一次，
适配器做不到的操作在界面上不出现。

它自带一套账号体系，不借用服务器的：短时效 JWT 加刷新轮换、TOTP 与一次性恢复码、
Argon2id 口令哈希、五级权限。审计日志用 SHA-256 链起来，改过或删掉一条会让链断开
（2026 年 9 月之前的构建写下的记录，哈希用的时间戳精度数据库存不下，验证必然失败——
链从升级后写下的第一条起可验）；存进去的服务器凭据用 AES-256-GCM 静态加密。

## 安装

没有软件包、没有镜像、也没有 release，只能自己编译。需要 Go 1.24.7+、Node 20.19+ / 22.13+ / 24+，
以及 PostgreSQL 14+（唯一支持的数据库）。

```bash
git clone https://github.com/Lynthar/XMPanel.git
cd XMPanel
```

```bash
sudo -u postgres psql -c "CREATE USER xmpanel WITH PASSWORD 'change-me';"
sudo -u postgres psql -c "CREATE DATABASE xmpanel OWNER xmpanel;"
```

```bash
cp config.example.yaml config.yaml
make generate-key          # 打印加密密钥，粘进 config.yaml
make deps
make build
./xmpanel
```

首次启动会打印一次初始 `admin` 口令，记得保存下来。

> **要从仓库根目录启动。** 前端是按相对路径从 `web/dist` 提供的，配置文件默认也是
> `./config.yaml`。写 systemd 单元记得设 `WorkingDirectory=`。

Prosody 那边需要先准备：装三个社区模块（`mod_http_admin_api`、`mod_tokenauth`、
`mod_http_oauth2`），给 `mod_tokenauth` 打一个小补丁，签一个 Bearer token，
再把本仓库的 `prosody/mod_admin_panel.lua` 装上。上游自带的 admin API 建账号时会返回
200 但**账号根本没建出来**，而且完全不暴露在线会话——那个额外模块就是为了解决这两点。

Synapse 那边只要一枚管理员账号的 access token（`register_new_matrix_user -a` 建的账号，
或管理员登录得到的）。地址填客户端监听，`/_matrix` 与 `/_synapse/admin` 都要能从那里访问。
把认证委派给 Matrix Authentication Service 的部署会被探测出来。此时管理员令牌由 MAS 签发
（`mas-cli manage issue-compatibility-token <管理员> --yes-i-want-to-grant-synapse-admin-privileges`，
再跑一次 provisioning 让 Synapse 认识这个会话的设备）；服务器条目带上 MAS 客户端（在 `clients:` 里以
`client_secret_basic` 声明、并列入 `policy.data.admin_clients` 的 OAuth 2.0 客户端）之后，
账号生命周期走 MAS 的 admin API；没有它，面板仍能列账号、设备和房间、注销设备、清除房间，
但不建号、不锁定、不停用、不改密。

Tuwunel 自己提供 Synapse 的 admin API，所以要的是同一种令牌：一枚服务器管理员的 access token，
管理员即它 admin room 的成员（第一个注册的账号，或在 admin room 里用 `!admin users` 命令提拔的）。
这里没有 MAS 的事：Tuwunel 不接受 MAS 令牌，它自带的新一代认证也不改变面板能做什么。

其他 Matrix 服务器（Continuwuity、Dendrite、Conduit 等）以「其他 Matrix 服务器」添加，给一枚管理员的 access token。
只用规范端点，所以没有账号列表：账号 tab 一次查一个 id。服务器声明了 `m.account_moderation`（规范 v1.18；
Continuwuity 与 Tuwunel 有，Synapse 1.161 还没有）就能锁定与挂起，服务器提供 `whois` 就能看到用户的连接。

## 用法

打开 `http://localhost:8080`，用 `admin` 登录。忘记口令时：

```bash
./xmpanel --reset-admin
```

它只重置 `admin` 这一个账号——换新口令、清掉 MFA、吊销它的全部会话——然后退出。

在 Servers 页添加服务器。一台服务器有两个地址：面板连过去的**管理 API 地址**
（通常是回环 URL，比如 `http://127.0.0.1:5280`）和账号所属的**域名**（XMPP 的 VirtualHost，或 Matrix 的 `server_name`）。
Prosody 那边域名会放进 HTTP Host 头，`mod_http_admin_api` 靠它选 VirtualHost，所以地址填 IP 没问题。
「测试连接」会探测服务器并报告它支持什么。

想脚本化可以直接调 API：

```bash
curl -X POST http://localhost:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<口令>"}'
```

非安全方法另需 `X-CSRF-Token`，值从 `csrf_token` cookie 读。

## 配置

工作目录下的 `config.yaml`，或者 `XMPANEL_CONFIG` 指向的位置。
`config.example.yaml` 是带注释的参考。

| 键 | 说明 |
|---|---|
| `database.dsn` | PostgreSQL 连接串 |
| `database.encryption_key` | base64 32 字节，**必填**：没有它面板拒绝启动，因为密钥丢了之后凭据就再也解不开 |
| `security.jwt.secret` | 至少 32 字符，强制。留空会每次启动现生成一个，重启后所有会话作废 |
| `security.cookies.secure_override` | `auto` / `always` / `never`——在卸载 TLS 的反代后面用 `always` |
| `security.rate_limit.trust_x_forwarded_for` | 只有在受信代理后面、且代理列进 `trusted_proxies` 才开，否则客户端能伪造源 IP。面板记录的客户端地址全看它——限流、登录锁定、会话与审计日志 |
| `server.address` | 默认 `:8080` |
| `monitor.sample_interval` | 每台启用的服务器多久探一次存活、延迟与计数，默认 `1m`。`/health` 读的就是最近一轮，所以它同时是该端点的陈旧上限 |
| `monitor.check_interval` | TLS 到期、DNS SRV、well-known 与联邦可达性多久查一次，默认 `6h` |
| `monitor.retention` | 采样保留多久，默认 `720h`（30 天）。按默认周期，一台服务器一个月约 2.5 MB |

放真实数据进去之前先把 JWT secret 设好；加密密钥启动时就会检查。

## 能力边界

- **房间管理只有 ejabberd 和 Synapse 有。** Prosody 的上游 API 不暴露房间。
- **XMPP 的分页是面板做的，不是服务器做的。** 两个 XMPP 适配器都是取全表再在内存里分页，
  账号特别多的服务器列表会慢。Synapse 由服务端分页。
- **Matrix 支持 Synapse 与 Tuwunel，而且不完整。** Synapse 在口令（legacy）认证下功能齐全；接了 Matrix Authentication Service 的部署，账号生命周期要有上面说的 MAS 客户端。
  删账号是停用（Matrix 没有删除），id 永久占用；删房间是启动服务端的后台清除，房间可能在列表里多留一会儿。
  不建房、没有全局设备列表。审核工具（彻底抹除、挂起、隐形封禁、媒体隔离、注册令牌、举报、房间封禁与清除、服务器通知、联邦状态）已有；
  抹除、隐形封禁、封禁与清除要 admin 角色并照原样输入目标 ID。服务器通知要求 Synapse 配置了 `server_notices`，面板事先探测不到：没配的话按钮会答「不支持」。
  Tuwunel 没有隐形封禁、举报和媒体隔离，设了 `mas_secret` 之后注册令牌归 MAS 管；面板在它那里不显示这些工具。
  其他服务器只有规范能给的那一小部分：查一个账号、锁定、挂起、看连接，没有列表，没有别的。
- **只支持 PostgreSQL**，没有 SQLite、没有 MySQL。
- **面板本身没有容器镜像。** 源码构建加 systemd；`smoke/` 下的 compose 只用来起测试服务器。
- **多个浏览器标签页同时刷新会触发 token 重用检测**，把那个用户的全部会话一起登出。

## 安全

会话用短时效 access token 加刷新轮换；同一个 refresh token 被用第二次，整个会话作废。
所有需要认证的写操作都做 CSRF 双提交校验。口令用 Argon2id 哈希。存进去的服务器凭据
用 AES-256-GCM 加密。

登录失败只记进数据库的审计日志，不写 stderr，所以 fail2ban 目前无从匹配。

没有私密漏洞报告渠道，敏感问题请不要发公开 issue。

## 许可证

Apache License 2.0 —— 见 [LICENSE](LICENSE)。Copyright (c) 2026 Lynthar。
