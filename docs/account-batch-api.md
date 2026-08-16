# 批量账号上传 API

## 接口

`POST /admin/accounts/batch`

该接口使用现有 Admin 鉴权。请求必须使用 `Content-Type: application/json`，请求体上限 16 MiB，单次最多 5000 个账号。

生产环境应通过 HTTPS 调用。接口层的凭据保护包括：

- password 和 token 只进入配置存储，不出现在响应中。
- `refresh_token` 不是该接口支持的字段；严格 JSON 校验会拒绝它，不会把其值映射、存储或回显为 token。
- 服务日志只记录提交、创建、更新、跳过和无效数量，不记录请求体、账号密码、token 或 refresh token。
- 响应包含 `Cache-Control: no-store` 和 `Pragma: no-cache`。
- WebUI 不预览 JSON 原文、password、token 或 refresh token；关闭弹窗或成功导入后会清空浏览器内的文件数据引用。

上述保护限制了传输、日志和响应中的凭据暴露，不代表静态加密。生产部署仍应限制 `data/` 目录和账户存储文件的访问权限。

## 请求格式

```json
{
  "on_duplicate": "skip",
  "dry_run": false,
  "defaults": {
    "enabled": true,
    "proxy_id": "proxy-main",
    "proxy_auto_route": true
  },
  "accounts": [
    {
      "email": "user-1@example.com",
      "password": "account-password",
      "name": "Primary 1",
      "remark": "team-a"
    },
    {
      "mobile": "+8613800000000",
      "token": "existing-upstream-token",
      "proxy_id": "proxy-backup",
      "enabled": false
    }
  ]
}
```

字段说明：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `accounts` | array | 是 | 账号数组，1 到 5000 条 |
| `on_duplicate` | string | 否 | `skip`（默认）或 `update` |
| `dry_run` | boolean | 否 | 仅校验和计算结果，不写入配置 |
| `defaults.enabled` | boolean | 否 | 条目未指定 enabled 时使用 |
| `defaults.proxy_id` | string | 否 | 条目未指定 proxy_id 时使用 |
| `defaults.proxy_auto_route` | boolean | 否 | 条目未指定 proxy_auto_route 时使用；update 模式也会应用 |
| `email` / `mobile` | string | 至少一个 | 账号标识；email 去重不区分大小写，手机号按规范化值去重 |
| `password` / `token` | string | 新账号至少一个 | 更新已有账号时可省略，省略即保留原凭据 |
| `name` / `remark` | string | 否 | 管理端显示信息 |
| `proxy_id` | string | 否 | 必须引用已存在的代理 ID |
| `proxy_auto_route` | boolean | 否 | 由健康节点池粘性分配出口；启用时账号必须保存 password |
| `enabled` | boolean | 否 | false 表示导入后保持手动停用 |

`update` 模式不会用空 password 或空 token 覆盖已有凭据；未提供 name、remark 或 proxy_id 时也会保留原值。每个条目独立校验，有效条目仍可写入，无效条目通过 results 返回原因。

## WebUI 导入

批量导入弹窗会在读取文件前检查 16 MiB 上限，解析后检查 `accounts` 不超过 5000 条，并在发送前再次检查最终 JSON 请求大小。这样可以在凭据文件离开浏览器前尽早阻止超限请求。

弹窗中的“导入默认值”会保留 JSON 文件已有的 `defaults`，并可覆盖以下默认字段：

- 默认启用状态：`defaults.enabled`。
- 默认出口代理：`defaults.proxy_id`；选择直连会写入空字符串。
- 默认自动路由：`defaults.proxy_auto_route`；全局自动路由未启用时不能在 WebUI 中新开此选项。

这些默认值只影响条目未显式指定相同字段的部分；条目级 `enabled`、`proxy_id` 和 `proxy_auto_route` 保持优先。选择“使用文件值”不会改写 JSON 文件中的对应 `defaults` 字段。自动路由仍要求账号保存 password。

## 响应格式

```json
{
  "success": false,
  "dry_run": false,
  "submitted": 3,
  "created": 1,
  "updated": 1,
  "skipped": 0,
  "invalid": 1,
  "total_accounts": 21,
  "results": [
    {"index": 0, "identifier": "user-1@example.com", "status": "created"},
    {"index": 1, "identifier": "+8613800000000", "status": "updated"},
    {"index": 2, "status": "invalid", "code": "validation_error", "detail": "email or mobile is required"}
  ]
}
```

只要存在无效条目，`success` 为 false，但 HTTP 状态仍为 200，调用方应读取每项 `status`。格式错误、Content-Type 错误、请求过大或策略值非法返回对应 4xx。

## 调用示例

```bash
curl http://127.0.0.1:5001/admin/accounts/batch \
  -H "Authorization: Bearer <admin-jwt>" \
  -H "Content-Type: application/json" \
  --data-binary @accounts.json
```

建议先把同一文件以 `dry_run: true` 调用一次，再执行实际导入。

## 选中账号批量操作

`POST /admin/accounts/batch/actions` 用于对已有账号执行同一种批量动作。它与上传接口分离，避免上传的重复处理策略影响已选择的账号。请求同样需要 Admin 鉴权和 `Content-Type: application/json`，每次最多 5000 个去重后的账号标识。

```json
{
  "identifiers": ["user-1@example.com", "+8613800000000"],
  "action": "set_proxy",
  "proxy_id": "proxy-main",
  "auto_route": false
}
```

`action` 支持：

- `set_proxy`：必须显式提供 `proxy_id`。空字符串表示直连；`auto_route: true` 启用粘性自动路由，空 `proxy_id` 会保留现有健康出口直到节点失效。
- `enable`：批量解除手动停用。
- `disable`：批量手动停用并从请求池移除。

`set_proxy` 会先验证所有账号、节点引用和账号密码；任一项校验失败时不会写入部分账号。通过校验后服务会清除发生出口变化账号的旧 Token、同步共享 Xray 路由，并以受限并发重新登录；登录失败会保留已变更的出口和空 Token，并通过响应明确报告。自动路由仍要求全局 `proxy_policy.auto_route_enabled=true` 和账号已保存 password。响应包含 `affected`、`route_changed` 以及 `relogin.attempted/succeeded/failed`，不回显密码或 Token。

## 实时账号状态

`GET /admin/accounts` 的每个条目新增以下运行态字段：

- `account_state`: 最终状态，可能为 `idle`、`busy`、`saturated`、`rate_limited`、`temporarily_muted`、`invalid_credentials`、`permanently_banned` 或 `disabled`。
- `health_state`: 健康状态；正常时为 `healthy`。
- `runtime_state`: 仅表示槽位活动状态，值为 `idle`、`busy` 或 `saturated`。
- `in_use`、`max_inflight`、`available_slots`、`utilization_percent`: 实时槽位数据。
- `health_reason`、`health_until`、`health_updated_at`: 临时冷却或上游健康异常详情。

`GET /admin/queue/status` 新增 `account_runtime` 和 `state_counts`。429 会进入默认 60 秒、最长 15 分钟的短期冷却，优先遵循上游 `Retry-After`，不会持久禁用账号；永久封禁和无效凭据仍会自动停用。

缓存命中率来自 `GET /admin/metrics/overview` 的全局 `cache.hit_rate`，不是单账号指标。
