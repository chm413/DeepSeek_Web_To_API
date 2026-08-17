# Xray 代理、订阅与健康检测

应用支持 SOCKS5/SOCKS5H，以及由 Xray 承载的 Shadowsocks（`ss://`）、VLESS、VMess、Hysteria2/HY2 节点。节点可手动添加，也可从机场订阅导入。

## 共享进程模型

所有启用账号实际引用的 Xray 节点共用一个 Xray 进程：

- 一个应用实例最多维护一个常驻 Xray 进程。
- 每个活动节点对应一个仅监听 `127.0.0.1` 的 SOCKS 入站和一条精确路由规则。
- 多个账号引用同一节点时不会重复创建入站。
- 未被启用账号引用的节点不会进入常驻配置。
- 单节点或批量测试可临时加入共享配置；完成后恢复账号路由集合。
- 节点、账号路由、核心设置或兜底策略变化时，应用原子重建共享配置并重启该进程。

SOCKS5/SOCKS5H 由 Go HTTP 客户端直接使用，不进入 Xray 配置；Shadowsocks 和其他核心节点由共享 Xray 进程承载。

## 自动下载

默认启用 Xray 自动下载。应用从官方 `XTLS/Xray-core` GitHub Releases 获取当前平台的压缩包，提取 Xray 可执行文件、`geoip.dat` 和 `geosite.dat`，默认保存到 `data/xray`。

查找顺序：

1. `proxy_core.xray_binary_path`。
2. `XRAY_BINARY_PATH` 环境变量。
3. 应用目录和 `bin` 子目录。
4. 系统 `PATH`。
5. 自动下载目录。

Docker 容器以非 root 用户运行，`/app/data` 可写；Compose 将其映射到宿主机 `./data`，因此下载结果位于 `/app/data/xray` 并跨重启保留。

```json
{
  "proxy_core": {
    "xray_binary_path": "",
    "runtime_dir": "",
    "startup_timeout_seconds": 10,
    "auto_download_disabled": false,
    "download_dir": "data/xray",
    "download_version": ""
  }
}
```

`download_version` 留空表示最新稳定版；也可固定为 `v26.3.27` 等明确版本。管理台可手动触发重新下载。

## 机场订阅

支持以下订阅内容：

- 一行一个 `ss://`、`vless://`、`vmess://`、`hysteria2://` 或 `hy2://` 链接。
- 上述 URI 列表的 base64 编码。
- Clash YAML 或 JSON 的 `proxies` 列表，包括 `type: ss` 或 `type: shadowsocks` 节点。

Shadowsocks 使用 SIP002 `ss://` 链接。Shadowsocks 插件传输不受此 Xray 集成支持：URI 中的 `plugin` 参数，以及 Clash 节点中的 `plugin` 和 `plugin-opts` 会被拒绝，而不会被忽略。
Clash 节点的 `udp`、`tfo`、`tcp-fast-open` 和 `skip-cert-verify: false` 为兼容性元数据，可保留在订阅中；`skip-cert-verify: true` 仍会被拒绝。未加引号的数值密码可导入，但需要保留前导零的密码应使用 YAML 引号。

订阅 URL 可能包含认证信息，按敏感字段处理。列表、读取和更新响应仅返回 `has_url`，不会回显 URL；编辑时 URL 留空保留已有值。

订阅更新会按稳定节点 ID 合并状态：

- 已存在节点保留健康检测和禁用状态。
- 新节点自动加入节点列表。
- 刷新会按有效出口配置做跨订阅和手工节点语义去重；等价 URI、显示名或
  订阅归属不同的节点只保留一条。响应中的 `skipped_duplicates` 说明本次跳过
  的数量，节点凭据和原始 URI 不会回显。
- 同一订阅的 URI 表示变化但出口配置等价时，保留旧节点 ID，避免账号绑定和
  健康状态被重置。
- 已从订阅删除且未被账号引用的节点会删除。
- 已从订阅删除但仍被账号引用的节点会保留并以 `subscription_removed` 原因禁用，避免账号路由静默指向未知节点。

## 定时更新和健康策略

```json
{
  "proxy_policy": {
    "auto_route_enabled": false,
    "health_check_enabled": true,
    "health_check_interval_minutes": 15,
    "auto_disable_after_failures": 3,
    "auto_enable_on_recovery": true,
    "fallback_proxy_id": "",
    "subscription_update_interval_minutes": 60,
    "test_concurrency": 4
  }
}
```

- 后台监控器每分钟检查到期任务。
- 节点连续失败达到阈值后以 `health_check_failed` 原因自动禁用。
- 健康检测禁用的节点恢复后可自动重新启用；手动禁用的节点不会被检测结果自动启用。
- 账号分配节点不可用时优先使用启用状态的 `fallback_proxy_id`；无可用兜底时使用直连。
- 每个订阅可以覆盖全局更新间隔，也可以分别关闭自动更新和更新后测试。

## 粘性自动路由

`proxy_policy.auto_route_enabled` 默认关闭。启用全局开关后，还需要为账号设置 `proxy_auto_route: true`；自动路由账号必须保存密码，因为出口节点变化后旧 Token 会被清除并通过新节点重新登录。

- 路由池只包含未禁用、至少测试过一次且最新测试成功的节点。单次测试失败会立即移出池，后续测试成功会重新加入，无需等待连续失败自动禁用阈值。
- 分配优先选择“已启用账号分配数”最少的节点，再按延迟和节点 ID 排序。手动与自动分配都计数；运行时临时封禁/限流不改变持久启用状态，因此仍计数。
- 分配具有粘性：只处理尚未分配的账号，或当前节点已经离开健康池的账号。普通负载变化不会搬迁健康账号，以保持出口 IP 稳定。
- 节点切换后服务端先清除旧 Token、同步共享 Xray 路由，再通过新出口重新登录并持久化新 Token。
- 自动路由账号没有可用节点时保持未分配并阻断上游请求，不会回退到直连。手动路由账号继续遵循 `fallback_proxy_id` 策略。
- 节点测试还会通过同一出口读取 Cloudflare trace，记录出口 IP、国家/地区代码和 colo；元数据探测失败不会把 DeepSeek 连通性判为失败。

## 管理 API

核心：

- `GET /admin/proxies/core`
- `PUT /admin/proxies/core`
- `POST /admin/proxies/core/download`

策略：

- `GET /admin/proxies/policy`
- `PUT /admin/proxies/policy`

订阅：

- `GET /admin/proxies/subscriptions`
- `POST /admin/proxies/subscriptions`
- `PUT /admin/proxies/subscriptions/{subscriptionID}`
- `DELETE /admin/proxies/subscriptions/{subscriptionID}`
- `POST /admin/proxies/subscriptions/{subscriptionID}/refresh`
- `POST /admin/proxies/subscriptions/refresh-all`

节点：

- `GET /admin/proxies`
- `POST /admin/proxies`
- `PUT /admin/proxies/{proxyID}`
- `DELETE /admin/proxies/{proxyID}`
- `POST /admin/proxies/test`
- `POST /admin/proxies/test-batch`
- `POST /admin/proxies/actions`

所有接口需要管理员认证。安全响应不会返回代理密码、节点 URI 或订阅 URL。

## 支持范围

| 类型 | 当前支持 |
| --- | --- |
| Shadowsocks | SIP002 `ss://`；插件传输不支持 |
| VLESS | TCP/RAW、WebSocket、gRPC、XHTTP/SplitHTTP、HTTPUpgrade；TLS、REALITY |
| VMess | 标准 base64 JSON、`alterId=0`；TCP、WebSocket、gRPC、XHTTP/SplitHTTP、HTTPUpgrade；TLS |
| Hysteria2 | `hysteria2://` 和 `hy2://`、TLS、auth |

为避免静默降级，当前会拒绝 `insecure=1`、`allowInsecure=1`、Hysteria2 `obfs`/`obfs-password`、`pinSHA256` 和非零 VMess `alterId`。

## 日志

应用日志记录共享 Xray PID、活动路由数、代理 ID、协议、本地端口、启动/停止和失败原因，但不记录节点 URI 或订阅 URL。Xray 运行日志保存在配置的 runtime 目录中，用于诊断核心配置和拨号错误。

官方来源：[XTLS/Xray-core](https://github.com/XTLS/Xray-core) 和 [Xray-core Releases](https://github.com/XTLS/Xray-core/releases)。
