# DeepSeek_Web_To_API API 文档

语言 / Language: [中文](API.md) | [English](API.en.md)

本文记录当前 Go 代码库实际暴露的 HTTP 接口。默认 Base URL 为 `http://127.0.0.1:5001`。

## 版本变更摘要（v1.0.8 ~ v1.0.12）

| 版本 | 变更摘要 |
|------|---------|
| **v1.0.8** | **自动删除远程会话**统一接入：`/v1/chat/completions`、`/v1/responses`、`/v1/messages` 三条 handler 链均在请求结束后执行 `AutoDeleteRemoteSession`。修复前，Claude Code 使用的 `/v1/messages` 端点即使 WebUI 开启"结束后全部删除"也不清理会话（Issue #20）。 |
| **v1.0.10** | **严格模型 allowlist（破坏性变更）**：移除模型族前缀启发式兜底（`gpt-` / `claude-` / `gemini-` 等前缀不再自动路由）。未在 `DefaultModelAliases` 或 WebUI Model Aliases 中的 id 返回 4xx。`deepseek-v4-vision` 从 `/v1/models` 隐藏并全端点封禁。 |
| **v1.0.12** | **429 弹性故障转移**：上游返回 429 时触发账号池切换，不消耗重试配额。峰值时段 429 对客户端的暴露率显著降低；仅当池中所有账号同时触发限流时，客户端才会收到 429。 |

## 基础规则

- 请求体：JSON 接口要求合法 UTF-8。
- 健康检查：`GET /healthz`、`HEAD /healthz`、`GET /readyz`、`HEAD /readyz`。
- 业务鉴权：`Authorization: Bearer <token>`、`x-api-key: <token>`；Gemini 兼容 `x-goog-api-key`、`?key=`、`?api_key=`。
- 托管账号模式：token 命中 `config.json` 的 `keys` 后使用账号池。
- 直通 token 模式：token 不在 `keys` 中时作为 DeepSeek token 直通。
- 管理鉴权：`POST /admin/login` 获取 JWT，其余管理接口使用 `Authorization: Bearer <jwt>` 或管理密钥。
- 缓存命中响应头：`X-DeepSeek-Web-To-API-Cache: memory|disk`、`X-DeepSeek-Web-To-API-Cache-Expires-At`。
- **模型 allowlist（v1.0.10+）**：`DefaultModelAliases` 覆盖约 100 个 OpenAI/Claude/Gemini 常见 id；自定义 id 需在 WebUI Settings → Model Aliases 添加显式映射，配置热重载无需重启。

## OpenAI 兼容接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/v1/models` | 返回可见模型列表（`deepseek-v4-vision` 已隐藏） |
| `GET` | `/v1/models/{model_id}` | 查询单个模型，支持 alias |
| `POST` | `/v1/chat/completions` | Chat Completions，支持流式；v1.0.8 起执行会话自动清理 |
| `POST` | `/v1/responses` | Responses，支持流式和响应暂存；v1.0.8 起执行会话自动清理 |
| `GET` | `/v1/responses/{response_id}` | 读取暂存 Response |
| `POST` | `/v1/files` | 上传文件（multipart，支持 `purpose` 字段）并写入兼容引用 |
| `POST` | `/v1/embeddings` | Embeddings 兼容响应（确定性，SharedAcrossCallers 缓存） |

同时支持 `/models`、`/chat/completions`、`/responses`、`/files`、`/embeddings` 根路径别名，以及 `/v1/v1/*` 兼容别名。

示例：

```bash
curl http://127.0.0.1:5001/v1/chat/completions \
  -H "Authorization: Bearer your-api-key-1" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5.5",
    "messages": [{"role": "user", "content": "你好"}],
    "stream": true
  }'
```

## Claude 兼容接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/anthropic/v1/models` | Claude 风格模型列表 |
| `POST` | `/anthropic/v1/messages` | Claude Messages，支持流式；v1.0.8 起执行会话自动清理 |
| `POST` | `/anthropic/v1/messages/count_tokens` | Token 估算（确定性，SharedAcrossCallers 缓存） |
| `POST` | `/v1/messages`、`/messages` | Messages 别名 |
| `POST` | `/v1/messages/count_tokens`、`/messages/count_tokens` | Count Tokens 别名 |

> **Claude Code 用户注意（v1.0.8 修复）**：v1.0.8 以前，即使 WebUI 开启"结束后全部删除"，通过 `/v1/messages` 发起的请求也不执行远程会话清理（Issue #20）。升级到 v1.0.8 后行为已与其他端点对齐。

示例：

```bash
curl http://127.0.0.1:5001/anthropic/v1/messages \
  -H "x-api-key: your-api-key-1" \
  -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-sonnet-4-6",
    "max_tokens": 1024,
    "messages": [{"role": "user", "content": "你好"}],
    "stream": true
  }'
```

## Gemini 兼容接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `POST` | `/v1beta/models/{model}:generateContent` | 非流式内容生成（SharedAcrossCallers 缓存） |
| `POST` | `/v1beta/models/{model}:streamGenerateContent` | 流式内容生成 |
| `POST` | `/v1/models/{model}:generateContent` | v1 别名 |
| `POST` | `/v1/models/{model}:streamGenerateContent` | v1 流式别名 |

示例：

```bash
curl "http://127.0.0.1:5001/v1beta/models/gemini-2.5-pro:generateContent?key=your-api-key-1" \
  -H "Content-Type: application/json" \
  -d '{
    "contents": [{"role": "user", "parts": [{"text": "你好"}]}]
  }'
```

## Admin 接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `POST` | `/admin/login` | 登录管理台 |
| `GET` | `/admin/verify` | 校验 JWT |
| `GET` / `POST` | `/admin/config` | 读取或更新完整配置（含 Model Aliases 热重载） |
| `POST` | `/admin/config/import` | 导入配置 |
| `GET` | `/admin/config/export` | 导出配置 |
| `GET` / `POST` | `/admin/accounts` | 查询或新增账号 |
| `POST` | `/admin/accounts/batch` | 批量校验、创建或更新账号；凭据不回显 |
| `POST` | `/admin/accounts/batch/actions` | 对已选账号批量设置出口、自动路由或启停；出口变化后自动重新登录 |
| `PUT` / `DELETE` | `/admin/accounts/{identifier}` | 更新或删除账号 |
| `PUT` | `/admin/accounts/{identifier}/proxy` | 设置手动节点或粘性自动路由，并在出口变化后重新登录 |
| `POST` | `/admin/accounts/test` | 测试单账号 |
| `POST` | `/admin/accounts/test-all` | 批量测试账号 |
| `GET` | `/admin/queue/status` | 账号池与队列状态 |
| `GET` / `POST` | `/admin/proxies` | 查询或新增代理 |
| `PUT` / `DELETE` | `/admin/proxies/{proxyID}` | 更新或删除代理 |
| `POST` | `/admin/proxies/test` | 测试 SOCKS 或 Xray 代理连通性 |
| `POST` | `/admin/proxies/test-batch` | 批量测试节点并更新自动路由健康池 |
| `GET` / `PUT` | `/admin/proxies/policy` | 查询或更新健康检测、自动路由与兜底策略 |
| `GET` / `PUT` | `/admin/proxies/core` | 查询 Xray core 状态或更新核心路径、运行目录和启动超时 |
| `GET` | `/admin/chat-history` | 分页查看历史记录 |
| `GET` | `/admin/chat-history/{id}` | 查看历史详情 |
| `DELETE` | `/admin/chat-history` | 清空历史记录 |
| `DELETE` | `/admin/chat-history/{id}` | 删除单条历史 |
| `PUT` | `/admin/chat-history/settings` | 修改历史保留策略 |
| `GET` | `/admin/metrics/overview` | 总览指标 |
| `GET` | `/admin/version` | 版本信息 |

账号路由接口请求体为 `{"proxy_id":"node-id","auto_route":false}`；自动路由使用 `{"auto_route":true}`，要求全局 `proxy_policy.auto_route_enabled=true` 且账号保存了密码。响应包含实际 `proxy_id`、`auto_route`、`route_changed` 和 `relogin`。`GET /admin/proxies` 的节点条目包含 `route_available`、`last_latency_ms`、`last_exit_ip`、`last_country`、`last_colo`、`assigned_account_count`、`auto_routed_account_count`，顶层 `route_pool` 是按当前分配优先级排序的健康节点列表。

## 缓存规则

缓存覆盖 OpenAI Chat/Responses/Embeddings、Claude Messages/CountTokens、Gemini GenerateContent/StreamGenerateContent。请求键按调用方、协议路径、查询参数、影响输出的请求头和规范化 JSON 请求体隔离。

以下场景不写入缓存：非 2xx、响应体过大、请求显式绕过、无法确定调用方、非缓存路径。

显式绕过：

```http
Cache-Control: no-cache
X-DeepSeek-Web-To-API-Cache-Control: bypass
```

## 更多文档

- [配置说明](docs/configuration.md)
- [Xray 代理协议](docs/xray-proxy.md)
- [API 兼容系统](docs/API%20Compatibility%20System/API%20Compatibility%20System.md)
- [客户端兼容性](docs/client-compat/README.md)
- [Prompt 兼容流程](docs/prompt-compatibility.md)
- [工具调用语义](docs/toolcall-semantics.md)
