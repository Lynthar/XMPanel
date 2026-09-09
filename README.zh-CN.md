<div align="center">

<img src="web/public/favicon.svg" alt="XMPanel" width="72">

# XMPanel

[![license](https://img.shields.io/github/license/Lynthar/XMPanel)](LICENSE)
[![go](https://img.shields.io/github/go-mod/go-version/Lynthar/XMPanel)](go.mod)

</div>

Prosody XMPP 服务器的自托管 Web 管理面板：账号与会话管理、RBAC、MFA、防篡改审计日志。Go + React。

[English](README.md) | 简体中文

> **施工中。** 还没有发过版，只能从源码构建。Prosody 这一侧已经在真实服务器上部署使用过；
> ejabberd 适配器代码写好了，但**没有对着真实的 ejabberd 验证过**。觉得有意思可以先关注，
> 但别指望拿到一个打包好的产品。

它挂在 XMPP 服务器外面，每台服务器单独登记、各自走适配器，所以可以同时管好几台。
管的是账号、在线会话和 MUC 房间。

它自带一套账号体系，不借用服务器的：短时效 JWT 加刷新轮换、TOTP 与一次性恢复码、
Argon2id 口令哈希、五级权限。审计日志用 SHA-256 链起来，改过或删掉一条会让链断开
（2026 年 9 月之前的构建写下的记录，哈希用的时间戳精度数据库存不下，验证必然失败——
链从升级后写下的第一条起可验）；存进去的 XMPP API key 用 AES-256-GCM 静态加密。

## 安装

没有软件包、没有镜像、也没有 release，只能自己编译。需要 Go 1.24.7+、Node 20+，
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

## 用法

打开 `http://localhost:8080`，用 `admin` 登录。忘记口令时：

```bash
./xmpanel --reset-admin
```

它只重置 `admin` 这一个账号——换新口令、清掉 MFA、吊销它的全部会话——然后退出。

在 Servers 页添加服务器。类型选 `prosody`，host 填 **XMPP 虚拟主机名**，不要填 IP：
`mod_http_admin_api` 是按 HTTP Host 头路由的，填 IP 一律 404。同机部署就在 `/etc/hosts`
里把域名映射到回环。

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
| `database.encryption_key` | base64 32 字节。**留空的话每次启动都会现生成一个**，重启后此前加密的列就读不回来了 |
| `security.jwt.secret` | 至少 32 字符，强制。留空同样有上面那个问题——所有会话作废 |
| `security.cookies.secure_override` | `auto` / `always` / `never`——在卸载 TLS 的反代后面用 `always` |
| `security.rate_limit.trust_x_forwarded_for` | 只有在受信代理后面、且代理列进 `trusted_proxies` 才开，否则客户端能伪造源 IP。面板记录的客户端地址全看它——限流、登录锁定、会话与审计日志 |
| `server.address` | 默认 `:8080` |

放真实数据进去之前，先把上面那两个密钥设好。

## 能力边界

- **ejabberd 适配器未经验证。** 它是照着文档写的，但没在真实的 ejabberd 上跑过，
  所以它声明的能力只能算「打算这么做」，不是「确认能做」。
- **MUC 房间管理只存在于 ejabberd 那一侧**——也就是未验证的那一侧。Prosody 的上游 API
  不暴露房间。
- **XMPP 账号可以建、可以列、可以删，但改不了。** 改密码的能力在适配器层有，
  但没有任何路由和界面能走到它。
- **只支持 PostgreSQL**，没有 SQLite、没有 MySQL。
- **没有 Dockerfile 也没有 compose。** 源码构建加 systemd。
- **多个浏览器标签页同时刷新会触发 token 重用检测**，把那个用户的全部会话一起登出。

## 安全

会话用短时效 access token 加刷新轮换；同一个 refresh token 被用第二次，整个会话作废。
所有需要认证的写操作都做 CSRF 双提交校验。口令用 Argon2id 哈希。存进去的 XMPP API key
用 AES-256-GCM 加密。

登录失败只记进数据库的审计日志，不写 stderr，所以 fail2ban 目前无从匹配。

没有私密漏洞报告渠道，敏感问题请不要发公开 issue。

## 许可证

Apache License 2.0 —— 见 [LICENSE](LICENSE)。Copyright (c) 2026 Lynthar。
