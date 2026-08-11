# 批量账号上传 API

## 接口

`POST /admin/accounts/batch`

该接口使用现有 Admin 鉴权。请求必须使用 `Content-Type: application/json`，请求体上限 16 MiB，单次最多 5000 个账号。

生产环境应通过 HTTPS 调用。接口层的凭据保护包括：

- password 和 token 只进入配置存储，不出现在响应中。
- 服务日志只记录提交、创建、更新、跳过和无效数量，不记录请求体、账号密码或 token。
- 响应包含 `Cache-Control: no-store` 和 `Pragma: no-cache`。
- WebUI 文件上传不预览 JSON 原文，关闭弹窗或成功导入后清空浏览器内的文件数据引用。

## 请求格式

```json
{
  "on_duplicate": "skip",
  "dry_run": false,
  "defaults": {
    "enabled": true,
    "proxy_id": "proxy-main"
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
| `email` / `mobile` | string | 至少一个 | 账号标识；email 去重不区分大小写，手机号按规范化值去重 |
| `password` / `token` | string | 新账号至少一个 | 更新已有账号时可省略，省略即保留原凭据 |
| `name` / `remark` | string | 否 | 管理端显示信息 |
| `proxy_id` | string | 否 | 必须引用已存在的代理 ID |
| `enabled` | boolean | 否 | false 表示导入后保持手动停用 |

`update` 模式不会用空 password 或空 token 覆盖已有凭据；未提供 name、remark 或 proxy_id 时也会保留原值。每个条目独立校验，有效条目仍可写入，无效条目通过 results 返回原因。

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

## 实时账号状态

`GET /admin/accounts` 的每个条目新增以下运行态字段：

- `account_state`: 最终状态，可能为 `idle`、`busy`、`saturated`、`rate_limited`、`temporarily_muted`、`invalid_credentials`、`permanently_banned` 或 `disabled`。
- `health_state`: 健康状态；正常时为 `healthy`。
- `runtime_state`: 仅表示槽位活动状态，值为 `idle`、`busy` 或 `saturated`。
- `in_use`、`max_inflight`、`available_slots`、`utilization_percent`: 实时槽位数据。
- `health_reason`、`health_until`、`health_updated_at`: 临时冷却或上游健康异常详情。

`GET /admin/queue/status` 新增 `account_runtime` 和 `state_counts`。429 会进入默认 60 秒、最长 15 分钟的短期冷却，优先遵循上游 `Retry-After`，不会持久禁用账号；永久封禁和无效凭据仍会自动停用。

缓存命中率来自 `GET /admin/metrics/overview` 的全局 `cache.hit_rate`，不是单账号指标。
