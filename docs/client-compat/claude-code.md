# Claude Code v2.x 深度兼容性规范

<cite>
文档类型：客户端兼容性工程规范  
目标仓库：`e:/ds2api`（ds2api — DeepSeek-web-to-API 代理）  
Claude Code 版本参考：v2.x（重点覆盖 v2.1.24 ~ v2.1.126，2026-05 调研时点）  
信息来源：Anthropic 官方文档、claude-code GitHub Issues（#1229、#5318、#16848、#23220、#25597、#30926）、LiteLLM 事件报告、社区逆向分析  
ds2api 版本变更记录：Issue #20（v1.0.8）、严格 allowlist（v1.0.10）、429 弹性故障转移（v1.0.12）  
</cite>

---

## 简介

Claude Code 是 Anthropic 官方发布的终端编码代理（npm 包 `@anthropic-ai/claude-code`）。当用户将 `ANTHROPIC_BASE_URL` 指向 ds2api 端点时，Claude Code 作为 HTTP 客户端向 ds2api 的 `/v1/messages` 和 `/v1/messages/count_tokens` 发送请求。

与普通 Anthropic SDK 调用相比，Claude Code 在以下维度存在显著的"超集"行为：系统提示的数组块格式、多个 `betas` 标识符、`cache_control` + `scope` 注入、`mcp_servers` 字段、系统提示归因头部、`context_management` 等扩展字段。这些差异是导致第三方代理（LiteLLM、ds2api 等）出现兼容性问题的根本原因。

本文档**不重复** `claude-coding-clients.md` 中的概述内容，专注于 v2.x 的工程细节。

---

## 项目结构

本文档对应的 ds2api 代码路径：

```
internal/httpapi/claude/
├── handler_routes.go            # 路由注册（/v1/messages 等多路别名）
├── handler_messages.go          # 请求入口、proxyViaOpenAI 分支
├── handler_messages_direct.go   # DeepSeek Web 直连分支
├── handler_stream_realtime.go   # 流式实时处理
├── standard_request.go          # normalizeClaudeRequest（核心规范化）
├── handler_utils.go             # normalizeClaudeMessages、buildClaudeToolPrompt
├── handler_utils_sanitize.go    # cache_control / binary 字段清洗
├── handler_session_affinity.go  # claudeSystemText（兼容 string/array）
├── handler_thinking_policy.go   # 思考块控制
├── handler_tokens.go            # /count_tokens 端点
├── convert.go                   # Claude→DeepSeek 模型转换
├── stream_runtime_core.go       # 流式事件解析
├── stream_runtime_emit.go       # SSE 事件写出
├── stream_runtime_finalize.go   # message_delta / tool_use 最终化
internal/translatorcliproxy/
├── bridge.go                    # 格式转换桥
├── stream_writer.go             # OpenAI→Claude SSE 实时翻译
internal/format/claude/
└── render.go                    # BuildMessageResponse（非流式响应组装）
```

---

## 核心组件

### 1. 精确请求体结构

Claude Code v2.x 向 `/v1/messages` 发送的完整请求体如下（以真实流量抓包为基础）：

```jsonc
{
  // ── 基础字段（必填）
  "model": "claude-opus-4-6",        // 或 claude-sonnet-4-6 / claude-haiku-4-5 等
  "max_tokens": 32000,               // 典型值；子代理可能更低
  "stream": true,                    // 几乎总是 true；count_tokens 请求不含此字段
  "messages": [ /* 见第2节 */ ],

  // ── 系统提示（数组格式，不是字符串）
  "system": [
    {
      "type": "text",
      "text": "<主系统提示，包含工具文档>",
      "cache_control": { "type": "ephemeral", "ttl": "1h" }
    },
    {
      "type": "text",
      "text": "<额外上下文块，例如 CLAUDE.md>",
      "cache_control": { "type": "ephemeral", "ttl": "5m" }
    }
  ],

  // ── 工具定义
  "tools": [
    {
      "name": "Bash",
      "description": "Execute a bash command in the terminal...",
      "input_schema": { "type": "object", "properties": { "command": {"type":"string"}, "timeout": {"type":"number"} }, "required": ["command"] },
      "cache_control": { "type": "ephemeral" }   // 仅最后一个工具携带
    }
    // ... 其余约 24 个内置工具，最后一个带 cache_control
  ],
  "tool_choice": { "type": "auto" },   // 始终 auto，不会出现 none/required

  // ── Beta 功能标识（见第3节）
  "betas": [
    "claude-code-20250219",
    "interleaved-thinking-2025-05-14",
    "advanced-tool-use-2025-11-20",     // v2.1.69+ 新增
    "effort-2025-11-24"
  ],

  // ── 上下文管理（扩展字段，v2.x 引入）
  "context_management": {
    "edits": [
      { "type": "clear_thinking_20251015", "keep": "all" }
    ]
  },

  // ── MCP 服务器（可选，见第4节）
  "mcp_servers": [
    {
      "type": "url",
      "url": "https://example.com/mcp",
      "name": "github",
      "authorization_token": "TOKEN"
    }
  ],

  // ── 服务等级（可选）
  "service_tier": "auto",   // "auto" 或 "standard_only"；Claude Code "快速模式" 下为 "auto"

  // ── 元数据（用于会话路由）
  "metadata": {
    "user_id": "{\"session_id\":\"sess_abc123\"}"   // Claude Code 有时序列化为 JSON 字符串
  }
}
```

**关键差异点汇总：**

| 字段 | 普通 SDK 用法 | Claude Code v2.x 行为 |
|------|-------------|----------------------|
| `system` | 字符串或省略 | **始终为对象数组**，每块含 `cache_control` |
| `tools[-1].cache_control` | 一般不含 | 末尾工具携带 `{"type":"ephemeral"}` |
| `betas` | 按需传入 | **始终包含**若干固定标识符（见第3节） |
| `context_management` | 不存在 | v2.x 引入，含编辑指令 |
| `mcp_servers` | 仅 MCP 场景 | MCP 启用时出现，需 beta 头 |
| `metadata.user_id` | 字符串用户 ID | 可能是序列化的 JSON 对象 |

---

### 2. 消息格式与 thinking 顺序

#### 消息数组

```jsonc
"messages": [
  {
    "role": "user",
    "content": [
      {
        "type": "text",
        "text": "help me refactor this function",
        "cache_control": { "type": "ephemeral", "scope": "turn" }  // v2.1.24+ 新增 scope 字段
      }
    ]
  },
  {
    "role": "assistant",
    "content": [
      {
        "type": "thinking",
        "thinking": "...",
        "signature": "EqQBCgIY..."   // 必须原样透传，不得修改
      },
      {
        "type": "tool_use",
        "id": "toolu_01abc",
        "name": "Bash",
        "input": { "command": "ls -la" }
      }
    ]
  },
  {
    "role": "user",
    "content": [
      {
        "type": "tool_result",
        "tool_use_id": "toolu_01abc",
        "content": "total 48\n..."
      }
    ]
  }
]
```

**严格顺序要求：** Anthropic API 要求 assistant 轮次中 `thinking` 块必须出现在 `tool_use` 块**之前**。Claude Code 在多轮工具调用中严格遵守此顺序，ds2api 在回放 assistant 消息时不得重排内容块顺序。

#### `scope` 字段（v2.1.24+）

从 Claude Code v2.1.24 起，`cache_control` 对象增加了 `"scope": "turn"` 字段，对应 `prompt-caching-scope-2026-01-05` beta 特性（2026-01 引入）：

```json
"cache_control": {
  "type": "ephemeral",
  "scope": "turn"
}
```

**这是导致 AWS Bedrock 等代理 400 错误的根本原因**——Bedrock 的严格模式校验不允许 `ephemeral` 对象中出现未知字段。ds2api 在转发到 DeepSeek 前应剥离 `scope` 字段。设置 `CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS=1` **不能**屏蔽该字段（因为它不属于"实验性 beta"的管控范围）。

---

### 3. `betas` 数组语义

`betas` 字段**在请求体中传递**（而非仅通过 `anthropic-beta` 头部）。Claude Code v2.x 各版本典型 betas 列表如下：

```
# v2.1.68（稳定版参考）
anthropic-beta: claude-code-20250219,interleaved-thinking-2025-05-14,effort-2025-11-24,adaptive-thinking-2026-01-28

# v2.1.69+（当前主线）
anthropic-beta: claude-code-20250219,interleaved-thinking-2025-05-14,advanced-tool-use-2025-11-20,effort-2025-11-24
```

| Beta 标识符 | 功能 | 代理影响 |
|-------------|------|---------|
| `claude-code-20250219` | Claude Code 客户端标识，解锁若干默认行为 | 代理应透传；不识别时应忽略而非报错 |
| `interleaved-thinking-2025-05-14` | 工具调用之间穿插 thinking 块 | Claude Opus 4.5/Sonnet 4.5 需要；4.6/4.7 已内置不需要 |
| `advanced-tool-use-2025-11-20` | 工具搜索工具、代码执行等高级工具 | v2.1.69+ 引入；Bedrock/LiteLLM 需过滤 |
| `effort-2025-11-24` | 推理"努力程度"控制 | 代理应透传或忽略 |
| `adaptive-thinking-2026-01-28` | 自适应思考模式 | 同上 |
| `prompt-caching-scope-2026-01-05` | `cache_control.scope` 字段支持 | 不识别此 beta 的代理应剥离 `scope` 字段 |

**双通道传递：** Claude Code 同时在 HTTP 头部 `anthropic-beta` 和请求体 `betas` 数组中传递同一组标识符（两者内容相同）。代理**必须透传 `anthropic-beta` 头部**，否则会丢失功能。

**`CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS=1` 的作用范围：** 该环境变量可屏蔽部分实验性 beta，但 `claude-code-20250219` 和 `advanced-tool-use-2025-11-20` 不受其控制。

---

### 4. `mcp_servers` 字段

Claude Code 在启用 MCP 服务器时向请求体注入 `mcp_servers` 数组，同时需要在 `anthropic-beta` 头部携带 `mcp-client-2025-11-20`（旧版 `mcp-client-2025-04-04` 已弃用）。

**当前版本（mcp-client-2025-11-20）格式：**

```json
{
  "mcp_servers": [
    {
      "type": "url",
      "url": "https://mcp.example.com/sse",
      "name": "github",
      "authorization_token": "Bearer token 或 OAuth access token"
    }
  ],
  "tools": [
    {
      "type": "mcp_toolset",
      "mcp_server_name": "github",
      "default_config": { "enabled": true },
      "configs": {
        "github_create_pr": { "enabled": true },
        "github_delete_repo": { "enabled": false }
      }
    }
  ]
}
```

**弃用版本（mcp-client-2025-04-04）格式（ds2api 需向后兼容）：**

```json
{
  "mcp_servers": [
    {
      "type": "url",
      "url": "https://mcp.example.com/sse",
      "name": "github",
      "authorization_token": "TOKEN",
      "tool_configuration": {
        "enabled": true,
        "allowed_tools": ["github_repo_search", "github_create_pr"]
      }
    }
  ]
}
```

**ds2api 现有处理逻辑：** `expandMCPServersAsTools()` 将 `mcp_servers` 展开为虚拟工具条目（格式 `<server>.<tool>`），注入系统提示让 DeepSeek 感知工具名称并输出 `tool_use` 块。调用路由由下游客户端（Claude Code）负责——ds2api 无需实际连接 MCP 服务器。该函数同时兼容新旧两种配置格式（`tool_configuration.allowed_tools` 和 `configs` 均已处理）。

**字段说明：**

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `type` | string | 是 | 目前只有 `"url"` |
| `url` | string | 是 | 必须以 `https://` 开头 |
| `name` | string | 是 | 唯一标识符，被 `mcp_toolset.mcp_server_name` 引用 |
| `authorization_token` | string | 否 | OAuth Bearer Token |
| `tool_configuration` | object | 否 | 旧版字段，已弃用 |

---

### 5. `cache_control` 注入点全览

Claude Code 在以下四个位置注入 `cache_control`，代理须正确处理每一处：

```
1. system[0]    → {"type": "ephemeral", "ttl": "1h"}   主系统提示（静态内容，长 TTL）
2. system[N]    → {"type": "ephemeral", "ttl": "5m"}   后续系统块（动态内容）
3. tools[-1]    → {"type": "ephemeral"}                 最后一个工具定义
4. messages[-2].content[-1]  → {"type": "ephemeral", "scope": "turn"}  最近用户消息（v2.1.24+）
```

**TTL 字段：** `"ttl": "1h"` 需要 `extended-cache-ttl-2025-04-11` beta 支持，ds2api 在转发前不用特殊处理——直接忽略 TTL 字段即可，DeepSeek 端不支持缓存语义。

**`scope: "turn"` 字段：** 在 `standard_request.go` 的 `normalizeClaudeRequest` 路径中，`sanitizeClaudeBlockForPrompt` 会自动剥离 `cache_control` 和 `cache_reference` 键，防止它们泄漏到最终的 DeepSeek 提示文本中（已实现）。

---

### 6. 系统提示归因头部（Attribution Block）

Claude Code v2.1.36+ 在每次请求的系统提示头部注入一小段归因文本，格式如下（通过 HTTP 头 `x-anthropic-billing-header` 传递）：

```
x-anthropic-billing-header: cc_version=2.1.126; cc_entrypoint=cli; cch=<per-request fingerprint>;
```

**代理影响：** 该指纹**每个请求都不同**，即使提示内容完全相同。如果代理基于完整请求体做缓存 key（例如响应缓存），该头部会导致缓存永远 miss。Anthropic 原生 API 在处理前会剥离该头部，因此不影响原生端的 prompt cache。

**对 ds2api 的影响：** ds2api 通过会话亲和性（`claudeSessionAffinityScope`）进行账号路由，使用 `X-Claude-Code-Session-Id` 头部或请求体指纹（`claudeFirstUserFingerprint`）作为 key，与 billing header 无关，不受干扰。

用户可设置 `CLAUDE_CODE_ATTRIBUTION_HEADER=0` 关闭归因头部。

---

### 7. 流式事件序列

Claude Code 期望的 SSE 标准事件流：

```
event: message_start
data: {
  "type": "message_start",
  "message": {
    "id": "msg_01xxx",
    "type": "message",
    "role": "assistant",
    "model": "claude-opus-4-6",
    "content": [],
    "stop_reason": null,
    "stop_sequence": null,
    "usage": { "input_tokens": 1200, "output_tokens": 0 }
  }
}

event: content_block_start
data: {"type": "content_block_start", "index": 0, "content_block": {"type": "thinking", "thinking": ""}}

event: content_block_delta
data: {"type": "content_block_delta", "index": 0, "delta": {"type": "thinking_delta", "thinking": "Let me analyze..."}}

event: content_block_stop
data: {"type": "content_block_stop", "index": 0}

event: content_block_start
data: {"type": "content_block_start", "index": 1, "content_block": {"type": "text", "text": ""}}

event: content_block_delta
data: {"type": "content_block_delta", "index": 1, "delta": {"type": "text_delta", "text": "Here is"}}

event: content_block_stop
data: {"type": "content_block_stop", "index": 1}

event: content_block_start
data: {"type": "content_block_start", "index": 2, "content_block": {"type": "tool_use", "id": "toolu_01abc", "name": "Bash", "input": {}}}

event: content_block_delta
data: {"type": "content_block_delta", "index": 2, "delta": {"type": "input_json_delta", "partial_json": "{\"command\":\"ls -la\"}"}}

event: content_block_stop
data: {"type": "content_block_stop", "index": 2}

event: message_delta
data: {
  "type": "message_delta",
  "delta": { "stop_reason": "tool_use", "stop_sequence": null },
  "usage": { "output_tokens": 145 }
}

event: message_stop
data: {"type": "message_stop"}
```

**关键细节：**

- `message_delta.usage.output_tokens` 是**累计值**，Claude Code 依赖此字段计算用量
- 工具输入使用 `input_json_delta` + `partial_json` 字符串片段（不是旧版的完整 `input` 对象）；初始 `content_block_start` 中的 `"input": {}` 是占位符
- `thinking` 块必须先于 `tool_use` 块出现（`stream_runtime_emit.go` 中的 `closeThinkingBlock()` 确保了这一点）
- 流中途错误使用 `event: error`，格式为 `{"type": "error", "error": {"type": "api_error", "message": "..."}}`（见 `sendError()`）
- keep-alive ping 使用 `event: ping` + `data: {"type": "ping"}`（ds2api 已在 `OpenAI→Claude` 流翻译路径中把 SSE 注释行 `:` 转换为 `ping`）

**`usage` 字段中的缓存计数（代理无需提供，Claude Code 能容错）：**

```json
"usage": {
  "input_tokens": 1200,
  "output_tokens": 145,
  "cache_creation_input_tokens": 800,   // 可选
  "cache_read_input_tokens": 400        // 可选
}
```

---

### 8. 错误信封格式

Claude Code 期望的错误 JSON 结构：

```json
{
  "type": "error",
  "error": {
    "type": "rate_limit_error",
    "message": "Rate limit exceeded. Please retry after 30 seconds."
  }
}
```

**完整错误类型枚举（来自 Anthropic 官方文档）：**

| HTTP 状态码 | `error.type` | Claude Code 处理行为 |
|------------|-------------|---------------------|
| 400 | `invalid_request_error` | 报错显示，不重试 |
| 401 | `authentication_error` | 报错提示重新配置 API Key |
| 402 | `billing_error` | 报错显示账单问题 |
| 403 | `permission_error` | 报错显示权限问题 |
| 404 | `not_found_error` | 报错显示，不重试 |
| 413 | `request_too_large` | 报错提示请求过大 |
| 429 | `rate_limit_error` | **自动重试**（指数退避，最多 10 次，初始间隔 2s） |
| 500 | `api_error` | 自动重试 |
| 504 | `timeout_error` | 自动重试 |
| 529 | `overloaded_error` | **自动重试**（初始间隔建议 4s，比 429 更激进的退避） |

**重要区别：** HTTP 529 (`overloaded_error`) 表示服务端过载，与 HTTP 429 (`rate_limit_error`) 是用户自身配额耗尽的语义不同。ds2api 在 DeepSeek 返回频率限制时应根据实际情况区分使用 429 或 529。

**流式错误（200 OK 后的错误）：** 已被消费 SSE 流的情况下，错误通过事件流传递：
```
event: error
data: {"type": "error", "error": {"type": "overloaded_error", "message": "Overloaded"}}
```

---

### 9. `/v1/messages/count_tokens` 端点

Claude Code **频繁调用** count_tokens 进行 token 预算计算（用于触发上下文压缩 compact）。

### 9.1 增量会话复用

`/v1/messages` 在 `auto_delete.mode=none` 时支持与 Chat/Responses 相同的上游
增量续接：服务端按 `X-Claude-Code-Session-Id`（或稳定的调用方、系统提示和
首条用户消息指纹）定位逻辑会话，严格校验历史前缀后复用 DeepSeek
`chat_session_id` 和父消息 ID。每次命中都发送新的强制输出格式提示词、当前工具
契约以及本轮新增 role blocks；不会把旧 transcript 再放进提示词。工具调用和
thinking 块会按实际输出记录，便于下一轮严格匹配。

分支编辑、工具/思考设置改变、并发占用或 pinned completion 失败都会放弃增量租约；
当前请求自动退回完整历史重放。缓存为进程内六小时 TTL、每个作用域最多八条分支，
不跨进程重启保存。

**请求格式（标准 `/v1/messages` body，去掉 `stream` 字段）：**

```json
{
  "model": "claude-opus-4-6",
  "messages": [ ... ],
  "system": [ ... ],
  "tools": [ ... ],
  "betas": ["claude-code-20250219", ...]
}
```

URL 有时带有 `?beta=true` 查询参数（`/v1/messages/count_tokens?beta=true`），应忽略此参数。

**响应格式：**

```json
{ "input_tokens": 12345 }
```

**404 / 501 容错行为：** 根据 LiteLLM Issue #13252 的实战报告，当 count_tokens 端点返回 404 时，Claude Code **不会崩溃**，但会失去基于 token 计数的智能压缩能力，降级为触发式压缩或无压缩。ds2api 已在 `handler_tokens.go` 中实现该端点（基于估算）。

---

### 10. Task 工具与子代理请求

Claude Code 的 `Task` 工具（内置子代理）派发独立会话，**通过相同的 `/v1/messages` 端点**发送请求，无特殊头部标记。

**子代理 vs 父代理的区别：**

- 子代理拥有**独立的上下文窗口**，不继承父代理的对话历史
- 子代理系统提示与父代理相同（工具定义也相同），因此**首次请求可复用父代理的 prompt cache**
- 子代理类型通过系统提示中的 `subagent_type` 字段区分（`"Explore"` 子代理为只读模式）
- 唯一从父到子的通信通道是 `Task` 工具的 `prompt` 字符串参数

**注意：** Claude Code 内部维护代理层级信息（main → subagent 层次、session_id、parent-child 关系），但**不在 API 请求中暴露**。ds2api 无法通过请求体区分主代理和子代理请求，只能通过 `X-Claude-Code-Session-Id` 头部的一致性判断同一会话（已在 `handler_session_affinity.go` 中处理）。

---

### 11. User-Agent 与请求指纹头部

**固定头部：**

```
anthropic-version: 2023-06-01
content-type: application/json
x-api-key: <用户的 ANTHROPIC_API_KEY>  // 或 Authorization: Bearer <ANTHROPIC_AUTH_TOKEN>
X-Claude-Code-Session-Id: <UUID，同一会话内固定>  // ds2api 用于会话亲和性路由
x-anthropic-billing-header: cc_version=2.1.xxx; cc_entrypoint=cli; cch=<per-req fingerprint>;
```

**anthropic-beta 头部（与 betas 数组内容相同）：**

```
anthropic-beta: claude-code-20250219,interleaved-thinking-2025-05-14,advanced-tool-use-2025-11-20,effort-2025-11-24
```

**User-Agent：** 来自 Anthropic TypeScript SDK 的 `x-stainless-*` 系列头部（如 `x-stainless-lang: js`、`x-stainless-runtime: node`）。官方未公开完整 User-Agent 字符串，但稳定的识别特征是 `X-Claude-Code-Session-Id` 头部的存在。

**`anthropic-dangerous-direct-browser-access` 头部：** 不由 Claude Code CLI 发送（该头部仅用于浏览器环境直连 Anthropic API，绕过 CORS 限制）。

---

### 12. 代理兼容性已知问题

| 问题现象 | 根本原因 | 解决方法 | 修复版本 |
|---------|---------|---------|---------|
| WebUI 开启"结束后全部删除"，但 Claude Code 会话仍在服务端堆积 | `AutoDeleteRemoteSession` 之前未接入 `/v1/messages` handler | 升级到 ds2api **v1.0.8**；`AutoDeleteRemoteSession` 共享 helper 已接入 claude handler 的 deferred cleanup 路径（Issue #20） | **v1.0.8 已修复** |
| 客户端模型 id 返回 4xx，之前可以用 | v1.0.10 移除了前缀启发式兜底 | 在 WebUI Settings → Model Aliases 添加映射；`DefaultModelAliases` 已覆盖约 100 个常见 id | v1.0.10（破坏性变更） |
| 峰值时段频繁收到 429 | 账号池限流 | v1.0.12 起上游 429 触发账号切换而不消耗重试配额，429 暴露率显著降低；若仍频繁，增加账号数量 | **v1.0.12 已改善** |
| `400: system.N.cache_control.ephemeral.scope: Extra inputs are not permitted` | 代理不支持 `cache_control.scope` 字段（v2.1.24+ 引入） | 在转发前剥离 `scope` 字段（ds2api 的 `sanitizeClaudeBlockForPrompt` 已剥离 `cache_control` 整体） | 历史版本已处理 |
| `400: invalid beta flag "advanced-tool-use-2025-11-20"` | Bedrock/旧版代理不识别 v2.1.69+ 新增 beta | 过滤该 beta 或设置 `CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS=1`（但该环境变量对此 beta 无效） | — |
| `400: invalid beta flag "interleaved-thinking-2025-05-14"` | Vertex AI / 部分旧版代理不支持 | 代理需过滤不识别的 beta 而非透传 | — |
| `/v1/messages/count_tokens` 返回 404 | 代理未实现该端点 | ds2api 已在 `handler_tokens.go` 实现；若未实现，Claude Code 会降级（不崩溃） | 历史版本已实现 |
| `mcp_servers` 参数被代理剥离 | 代理不知道如何处理 | ds2api 通过 `expandMCPServersAsTools()` 将其展开为虚拟工具条目 | 历史版本已处理 |
| 每次请求都 miss 响应缓存 | `x-anthropic-billing-header` 中的 per-req 指纹每次不同 | 响应缓存的 key 不应包含该头部；用户可设置 `CLAUDE_CODE_ATTRIBUTION_HEADER=0` | — |
| thinking 块在 tool_use 之后 | 代理重排了内容块顺序 | 严格保持 thinking → tool_use 顺序（ds2api `stream_runtime_emit.go` 中已通过 `closeThinkingBlock()` 保证） | 历史版本已处理 |
| 工具或思考超过 120 秒后 Claude Code 中止 workflow，agent 在 deepseek 这边其实还在跑 | **这是 Claude Code 客户端自己的工具超时**，与 ds2api 服务端 7200s 超时无关 | 在启动 Claude Code 前设置环境变量：`BASH_DEFAULT_TIMEOUT_MS=600000`（默认工具超时 10 min）+ `BASH_MAX_TIMEOUT_MS=1800000`（上限 30 min）。这两个变量控制 Bash / 长时工具的客户端等待窗口，与 ds2api 的 `http_total_timeout_seconds` 是两条独立的超时链 | — |
| 思考 / 工具超时后，**之后的请求完全没被发到 deepseek** | Claude Code abort 旧请求时关掉的是它本地的 fetch context，但 ds2api 端的会话亲和（session affinity）仍然把后续请求绑到原账号，账号此时正在等待上一次响应；新请求会排队到 `account_max_inflight` 上限触发 429 / 等待超时 | ds2api v1.0.3-cnb 已经在 `internal/responsecache/cache.go` 加了 `single-flight in-flight dedup` 缓解；进一步建议：调小 `account_max_inflight`（默认 2，可在 admin 控制台改成 1）让请求快速失败而不是无限排队，配合上面的 `BASH_*` 超时设置 | v1.0.3+ |

> **Issue #20（v1.0.8 重要修复）详解：**  
> 根因：`AutoDeleteRemoteSession` 共享 helper 已经存在于 chat 和 responses handler 的 deferred cleanup 中，但 claude handler（`/v1/messages`）遗漏了这个接入点。Claude Code 几乎专用 `/v1/messages` 端点，导致即使 WebUI 开启"结束后全部删除"，Claude Code 发起的会话从不被清理。  
> 修复路径：将 `AutoDeleteRemoteSession` 接入 claude handler 的 deferred cleanup（与 chat/responses 对齐）。  
> 验证方法：开启 WebUI"结束后全部删除"，用 Claude Code 发起若干请求，然后在 admin 面板或直接查看 DeepSeek Web 会话列表，确认请求完成后会话被删除而非持续累积。

> **v1.0.2 → v1.0.3 升级强烈建议**：v1.0.2 的 `http_total_timeout_seconds` 默认仅 120 s，长时思考请求会在服务端硬切断；v1.0.3 起默认 7200 s（含 `StreamIdleTimeout` / `MaxKeepaliveCount` / 单飞 `inflightWaitTimeout` 全套对齐）。Pro 推理模型 + 长上下文场景几乎必须升级到 v1.0.3 才能避免服务端 120 s 限制叠加 Claude Code 客户端 120 s 限制造成的双重斩首。

> **v1.0.8 升级建议（Claude Code 用户必读）**：若你开启了 WebUI"结束后全部删除"，v1.0.8 以前的版本在 Claude Code 使用 `/v1/messages` 端点时不执行远程会话清理（Issue #20），会话在 DeepSeek Web 侧持续堆积。升级到 v1.0.8 可彻底解决此问题。

---

## 架构总览

```
Claude Code CLI
    │
    │  POST /v1/messages
    │  Headers: anthropic-version, anthropic-beta, X-Claude-Code-Session-Id
    │  Body: {model, system:[], messages:[], tools:[], betas:[], mcp_servers:?, context_management:?}
    ▼
ds2api /internal/httpapi/claude/
    ├── handler_routes.go          注册 /v1/messages + 多别名路由
    ├── handler_messages.go        入口：anthropic-version 补全、路径选择
    │       │
    │       ├── handleDirectClaudeIfAvailable()  →  DeepSeek Web 直连
    │       │       ├── normalizeClaudeRequest()  ← standard_request.go
    │       │       │   ├── claudeSystemText()    system string/array 统一提取
    │       │       │   ├── expandMCPServersAsTools()  mcp_servers 展开
    │       │       │   └── injectClaudeToolPrompt()   工具注入系统提示
    │       │       └── handleClaudeStreamRealtime() / handleDirectClaudeNonStream()
    │       │               └── stream_runtime_*.go    SSE 事件发射
    │       │
    │       └── proxyViaOpenAI()  →  OpenAI 兼容后端代理
    │               └── translatorcliproxy/  Claude⇄OpenAI 格式转换
    │
    └── handler_tokens.go          /count_tokens 估算（localCountClaudeInputTokens）
```

---

## 详细组件分析

### 系统提示处理链

**输入：** Claude Code 发送 `system: [{type, text, cache_control}, ...]`  
**处理：** `claudeSystemText()` 统一将 string / map / array 三种形式转为纯文本  
**输出：** 拼接后的字符串写入 DeepSeek 的 system prompt  
**剥离：** `sanitizeClaudeBlockForPrompt()` 删除 `cache_control` 和 `cache_reference` 字段，防止元数据泄漏到提示  

**问题所在：** `normalizeClaudeRequest()` 中的 `if systemText := claudeSystemText(req["system"]); systemText != ""` 处理了 array 格式，但从 `payload` 中存储的是提取后的纯字符串（`payload["system"] = systemText`）。因此，如果 ds2api 将请求透传到真实 Anthropic API，系统提示数组格式会被丢失。但由于 ds2api 始终转向 DeepSeek，这不是问题。

### 工具处理链

**输入：** `tools: [{name, description, input_schema, cache_control?, defer_loading?, type?}]`  
**处理：** `buildClaudeToolPrompt()` 序列化为文本格式注入系统提示  
**特殊字段：** `defer_loading: true`（延迟加载工具，不在初始系统提示中展示）、`cache_control`（仅末尾工具携带，`sanitizeClaudeBlockForPrompt` 负责清理）  
**MCP 工具集：** `type: "mcp_toolset"` 的条目由 `expandMCPServersAsTools()` 处理，转换为 `<server>.<tool>` 格式的虚拟工具  

### thinking 块处理

- **流式模式：** 由 `stream_runtime_emit.go` 发射 `thinking_delta` 事件，先于 `text_delta`
- **非流式模式：** 由 `format/claude/render.go` 的 `BuildMessageResponse()` 在 content 数组中以 `{"type":"thinking","thinking":"..."}` 形式返回
- **剥离控制：** `applyClaudeDirectThinkingPolicy()` 决定是否向客户端暴露 thinking 内容；不暴露时调用 `stripClaudeThinkingBlocks()`
- **redacted_thinking：** 在 `normalizeClaudeMessages()` 中被静默跳过（`case "redacted_thinking", "cache_edits": continue`），不透传给 DeepSeek

---

## 结论

Claude Code v2.x 相对于"标准 Anthropic SDK 用法"的核心超集特性：

1. **`system` 字段始终为对象数组**（非字符串），每块含 `cache_control`
2. **`betas` 数组始终存在**，v2.1.69+ 包含 `advanced-tool-use-2025-11-20`
3. **`cache_control.scope: "turn"`** 自 v2.1.24 起出现，非 Anthropic 原生 API 会报 400
4. **`mcp_servers` + `mcp_toolset`** 两阶段配置（当前版 beta `mcp-client-2025-11-20`）
5. **`X-Claude-Code-Session-Id` 头部**用于会话亲和性
6. **`/v1/messages/count_tokens`** 频繁调用，404 时降级但不崩溃
7. **thinking → tool_use 严格顺序**在多轮工具调用时必须维持

---

## ds2api 适配 Checklist

以下各项均已结合 ds2api 实际代码状态标注优先级和当前实现状态。

### P0 — 已实现，需验证健壮性

| 编号 | 改动要点 | 文件 | 状态 |
|------|---------|------|------|
| P0-1 | `system` 字段 string/array 统一提取（`claudeSystemText`） | `handler_session_affinity.go:185` | ✅ 已实现 |
| P0-2 | `cache_control` / `cache_reference` 从块中剥离，防止泄漏到提示 | `handler_utils_sanitize.go:34` | ✅ 已实现 |
| P0-3 | `mcp_servers` 展开为虚拟工具（`expandMCPServersAsTools`） | `standard_request.go:42` | ✅ 已实现，含新旧格式兼容 |
| P0-4 | thinking 块先于 tool_use 块输出（`closeThinkingBlock` 在 `emitTextDelta` 前调用） | `stream_runtime_emit.go:185` | ✅ 已实现 |
| P0-5 | `/v1/messages/count_tokens` 端点存在并返回 `{"input_tokens": N}` | `handler_tokens.go`, `handler_routes.go` | ✅ 已实现（估算） |
| P0-6 | 错误信封格式 `{"type":"error","error":{"type":"...","message":"..."}}` | `handler_errors.go` | ✅ 已实现（需确认 `overloaded_error` type 字符串） |
| P0-7 | WebUI"结束后全部删除"在 `/v1/messages` 正确触发远程会话清理（Issue #20） | claude handler deferred cleanup，`AutoDeleteRemoteSession` | ✅ **v1.0.8 已修复** |
| P0-8 | 上游 429 时触发账号切换（不消耗重试配额） | 账号池调度层 | ✅ **v1.0.12 已实现** |

### P1 — 存在缺口，应尽快修复

| 编号 | 改动要点 | 文件 | 当前问题 |
|------|---------|------|---------|
| P1-1 | 剥离 `cache_control.scope` 字段（防止转发给 DeepSeek 时出错）以及 `ttl`、`cache_edits` 等扩展字段 | `handler_utils_sanitize.go` | `sanitizeClaudeBlockForPrompt` 已删除整个 `cache_control` key，但如果 ds2api 日后需要透传 `cache_control` 到某些后端（如真实 Anthropic API），则需精细化：保留 `type` 字段，剥离 `scope`/`ttl`（或不被支持的字段） |
| P1-2 | `betas` / `context_management` / `defer_loading` 等 Claude Code 专有字段不应泄漏到 DeepSeek 提示文本或报错 | `standard_request.go`, `handler_utils.go` | `clear_thinking*` edits 会应用到规范化历史，其余不支持的 edits 被容忍且不泄漏 |
| P1-3 | `message_delta.usage` 中必须包含 `output_tokens`（Claude Code 依赖此字段统计用量） | `stream_runtime_finalize.go:68` | ✅ 已实现，但需确认 `input_tokens` 在 `message_start` 中的准确性 |
| P1-4 | 工具流式输入使用 `input_json_delta` + `partial_json`（不是旧版整体 `input` 对象） | `stream_runtime_finalize.go:104` | ✅ 已实现 `input_json_delta`，需确认 `content_block_start` 中的 `"input": {}` 占位符 |
| P1-5 | `count_tokens` 端点：`?beta=true` 查询参数应被忽略（路由正则不含 query string，Go HTTP 已自动处理） | `handler_routes.go` | 已通过 chi 路由处理，无需额外修改 |
| P1-6 | 在 `errors.go` 中补充 `overloaded_error` 的映射（当前 `claudeErrorCode` 没有 529 的 case） | `handler_errors.go:23` | 缺少 `case 529: code = "overloaded_error"` |

### P2 — 增强建议

| 编号 | 改动要点 | 文件 | 说明 |
|------|---------|------|------|
| P2-1 | `redacted_thinking` 块静默跳过（已跳过），同时 `signature_delta` SSE 事件也应无害忽略 | `stream_runtime_core.go` | 当前流式解析器可能不识别 `signature_delta` 事件类型，需验证不报错 |
| P2-2 | 响应缓存（如有）的 key 设计应排除 `x-anthropic-billing-header` | 视缓存实现位置而定 | 该头部每请求变化，会击穿任何基于完整请求 header 的缓存 |
| P2-3 | `X-Claude-Code-Session-Id` 头部已在会话亲和性中使用；考虑在日志中记录该值以辅助调试 | `handler_session_affinity.go` | 低优先级，仅改善可观测性 |
| P2-4 | `betas` 数组中已知的 Claude Code 专有标识符（`claude-code-20250219` 等）应从错误提示中排除（不向用户展示"不支持的 beta"警告） | `handler_messages.go` 或中间件层 | 目前 ds2api 不校验 beta 标识符，无需额外处理 |
| P2-5 | `service_tier: "auto"` 字段被忽略属于正常行为（ds2api 没有 Priority Tier）；若将来需要，可在响应 usage 中加 `"service_tier": "standard"` | `format/claude/render.go` | 低优先级，Claude Code 对此字段无强依赖 |

### 最优先单行修复

在 `handler_errors.go` 的 `claudeErrorCode` 函数中补充 529 状态码映射：

```go
// handler_errors.go — claudeErrorCode 函数中补充：
case 529:
    code = "overloaded_error"
```

同时，如果 ds2api 在 DeepSeek 返回过载/频率限制时需要返回 529 而非 429，应在 `writeClaudeCompletionCallError` 的调用链中确认正确的 HTTP 状态码选择。

---

*章节来源：Anthropic 官方文档（platform.claude.com）、claude-code GitHub Issues #1229/#5318/#16848/#23220/#25597/#30926、LiteLLM 事件报告（docs.litellm.ai/blog/claude-code-beta-headers-incident）、code.claude.com/docs/en/llm-gateway、ds2api 源码（e:/ds2api/internal/httpapi/claude/*）。*
