# OpenAI Codex CLI 兼容性集成简报

> 适用版本：Codex CLI ≥ 0.40（Rust 重写版，2025 年起），最新确认版本 0.128.x（2026-04）  
> 目标：让 ds2api 成为 Codex CLI 的透明替换后端  
> ds2api 版本变更：v1.0.8 `/v1/responses` 自动删除会话修复 · v1.0.10 严格模型 allowlist · v1.0.12 429 弹性故障转移

---

## 背景说明

**OpenAI Codex CLI**（github.com/openai/codex）是 OpenAI 于 2025 年推出的开源终端编码智能体，用 Rust 编写，与 2021 年的同名"Codex 补全模型"完全不同。本文档仅讨论该 CLI 工具（`codex` 命令），不涉及旧版模型 API。

---

## 1. API 接口面

### 1.1 主协议：Responses API（唯一）

Codex CLI **从 2026 年 2 月起已完全移除** `chat/completions` 支持：

- 旧版（0.40 之前）：`wire_api = "chat"` 走 `POST /v1/chat/completions`
- 当前版（0.40+）：**仅** `POST /v1/responses`（Responses API）

官方讨论 [Deprecating `chat/completions` support #7782](https://github.com/openai/codex/discussions/7782) 指出，移除旧协议是为了"加速功能开发、减少兼容回归"。2026 年 2 月后保留 `wire_api = "chat"` 的配置会触发硬错误而非仅警告。

### 1.2 /v1/responses 请求体核心字段

```json
{
  "model": "gpt-5.4",
  "input": [...],          // 消息数组或纯字符串
  "instructions": "...",   // 系统提示，对应旧版 system message
  "tools": [...],          // 工具定义（见第 2 节）
  "reasoning": {
    "effort": "medium",    // low | medium | high | xhigh
    "summary": "auto"      // 是否返回推理摘要
  },
  "stream": true,
  "store": true,           // 是否在服务端存储对话，用于 previous_response_id
  "max_output_tokens": 32768,
  "truncation": "auto",    // 自动截断上下文
  "context_management": {
    "compact_threshold": 0.85
  },
  "previous_response_id": "resp_xxx" // 上下文链接（可选）
}
```

**`input` 数组**中每个条目类型（`type` 字段）：

| type 值 | 含义 |
|---------|------|
| `message` / `input_message` | 普通用户/开发者/系统消息 |
| `function_call` | 模型输出的工具调用（在多轮历史中） |
| `function_call_output` | 工具调用结果（客户端返回给模型） |
| `reasoning` | 推理摘要条目（服务端插入，含 `encrypted_content`） |
| `compaction` | 上下文压缩条目（含不透明 `encrypted_content`） |

消息的 `role` 可为 `"user"` / `"developer"`（等价于旧版 `system`） / `"assistant"`。

### 1.3 是否调用 Anthropic API

**否。** Codex CLI 本身不直接调用 Anthropic Messages API。它是纯 OpenAI Responses API 客户端。官方支持的外部后端只有：OpenAI、Azure OpenAI、Amazon Bedrock（via SigV4）、Ollama（responses 格式），以及任意兼容 Responses API 的自定义 `base_url`。

### 1.4 基础 URL 配置方式

两种方式均支持，**优先级：环境变量 > config.toml**：

```bash
# 方式一：环境变量（一次性覆盖）
export OPENAI_BASE_URL="https://your-ds2api.example.com/v1"

# 方式二：config.toml 全局覆盖
# 文件位置：~/.codex/config.toml
openai_base_url = "https://your-ds2api.example.com/v1"
```

也可以定义命名 provider，灵活控制 `wire_api`：

```toml
[model_providers.ds2api]
name        = "ds2api proxy"
base_url    = "https://your-ds2api.example.com/v1"
env_key     = "OPENAI_API_KEY"
wire_api    = "responses"        # 必须是 "responses"，不能是 "chat"

[profiles.ds]
model          = "gpt-5.4"
model_provider = "ds2api"
```

切换命令：`codex --profile ds "refactor auth module"`

---

## 2. 工具调用格式

### 2.1 Responses API 工具定义结构

Responses API 与 Chat Completions API 的工具定义格式**不同**，字段层级发生了变化：

```json
// Responses API 格式（Codex 使用）
{
  "type": "function",
  "name": "apply_patch",
  "description": "Apply a structured diff patch to files",
  "parameters": {
    "type": "object",
    "properties": { ... },
    "required": [...]
  },
  "strict": true
}

// Chat Completions API 格式（旧版，Codex 已废弃）
{
  "type": "function",
  "function": {
    "name": "apply_patch",
    "description": "...",
    "parameters": { ... }
  }
}
```

关键区别：Responses API 中 `name`/`description`/`parameters` **直接位于顶层**，不再嵌套在 `function` 子对象内。

### 2.2 内置工具列表

Codex CLI 向模型注入的主要工具：

| 工具名 | 类型 | 用途 |
|--------|------|------|
| `shell` | `function` | 执行 shell 命令（PowerShell/bash），主要操作接口 |
| `apply_patch` | `function` | 提交结构化文件差异补丁（V4A diff 格式），支持 `create_file` / `update_file` / `delete_file` 操作 |
| `read_file` | `function` | 读取文件内容 |
| `list_dir` | `function` | 列出目录内容 |
| `glob_file_search` | `function` | glob 模式文件搜索 |
| `rg` / 搜索工具 | `function` | 代码搜索（ripgrep 封装） |
| `todo_write` / `update_plan` | `function` | 任务规划记录 |

**非标准工具类型**：

- `computer_use`：macOS 上的桌面视觉自动化（截图 + 点击），仅在桌面 App 版中启用
- **MCP 工具**：通过 `mcp_servers` 配置的第三方 Model Context Protocol 服务器，工具调用时以普通 `function` 格式包装，但包含 `outputSchema` 元数据

### 2.3 工具调用输出项格式（Responses API）

模型在流中输出工具调用时，是作为 `output_item` 发射：

```json
{
  "type": "function_call",
  "id": "fc_xxxx",
  "call_id": "call_xxxx",
  "name": "shell",
  "arguments": "{\"command\": \"ls -la\"}"
}
```

客户端执行后，通过 `input` 数组将结果送回：

```json
{
  "type": "function_call_output",
  "call_id": "call_xxxx",
  "output": "total 48\ndrwxr-xr-x ..."
}
```

---

## 3. 流式传输行为

### 3.1 SSE 帧格式

Codex CLI 使用标准 SSE 格式，每个事件两行加空行：

```
event: response.created
data: {"id":"resp_xxx","object":"response","..."}

event: response.output_item.added
data: {"item":{"type":"message","id":"msg_xxx"},"sequence_number":1}

event: response.output_text.delta
data: {"delta":"Hello","sequence_number":2}

event: response.output_text.done
data: {"text":"Hello world","sequence_number":99}

event: response.completed
data: {"response":{"id":"resp_xxx","usage":{"input_tokens":150,"output_tokens":42}}}

data: [DONE]
```

### 3.2 关键事件列表（完整生命周期）

| 事件名 | 触发时机 |
|--------|---------|
| `response.created` | 请求开始，响应对象创建 |
| `response.in_progress` | 推理进行中 |
| `response.output_item.added` | 新输出条目开始（文本、工具调用等） |
| `response.content_part.added` | 内容段开始 |
| `response.output_text.delta` | 文本增量片段（可多次） |
| `response.output_text.done` | 文本条目完成 |
| `response.function_call_arguments.delta` | 工具调用参数增量 |
| `response.function_call_arguments.done` | 工具调用参数完成 |
| `response.output_item.done` | 输出条目完成 |
| `response.reasoning_summary_text.delta` | 推理摘要增量（reasoning 模型） |
| `response.reasoning_summary_text.done` | 推理摘要完成 |
| `response.completed` | **响应完成（含 usage）** |
| `response.incomplete` | 触发截断停止 |
| `response.failed` | 错误终止 |
| `error` | 协议级错误 |

**关键实现要点**：

1. `usage`（token 统计）**仅在 `response.completed` 事件**中出现，不在增量事件中累加
2. delta 必须按 `sequence_number` 顺序追加；`.done` 事件才携带权威最终文本
3. 流结束标志是 `data: [DONE]\n\n`（无 `event:` 前缀），跟在 `response.completed` 之后
4. 每个事件 data 中包含 `sequence_number` 整数字段

### 3.3 并行工具调用

Codex CLI 启用并行工具调用时，`POST /v1/responses` 请求中设置 `parallel_tool_calls: true`。流中多个 `function_call` output_item 可并发出现，通过各自的 `call_id` 区分。

---

## 4. HTTP 请求头

### 4.1 标准头

```
Authorization: Bearer <OPENAI_API_KEY>
Content-Type: application/json
Accept: text/event-stream       （流式请求）
```

### 4.2 User-Agent

Codex CLI（Rust 版）发出的 `User-Agent` 形如：

```
codex/<version> (rust; <os>/<arch>)
```

例如 `codex/0.125.0 (rust; linux/x86_64)`。具体字符串随版本变化，不应依赖精确匹配。

### 4.3 `OpenAI-Beta` 等特殊头

**当前版本不发送 `OpenAI-Beta` 头**。Responses API 已从测试版升级为正式 API，不再需要该头。旧版 TypeScript 实现（0.3.x 时期）在使用 Assistants beta 时曾发送，Rust 重写版已移除。

`OpenAI-Organization` 头视账户配置注入，非必须字段，ds2api 可选择透传或忽略。

### 4.4 自定义头支持

config.toml 支持为 provider 添加任意 HTTP 头：

```toml
[model_providers.ds2api]
http_headers = { "X-Custom-Auth" = "token123" }
```

ds2api 应对未知头保持容忍，不做强制校验。

---

## 5. 模型名称规范

### 5.1 默认与推荐模型（截至 2026-05）

| 模型名 | 用途 |
|--------|------|
| `gpt-5.5` | 最新旗舰，复杂编码/研究（需 ChatGPT 登录，暂不支持 API Key） |
| `gpt-5.4` | **当前 API Key 用户首选**，强推理 + 工具调用 |
| `gpt-5.4-mini` | 轻量快速，子智能体任务 |
| `gpt-5.3-codex` | 早期旗舰编码模型 |
| `gpt-5.3-codex-spark` | 极速迭代预研版 |
| `gpt-5.1-codex` | 历史版本 |
| `gpt-5.1-codex-mini` | 历史轻量版 |

### 5.2 模型名覆盖方式

用户可通过以下方式覆盖默认模型：

```bash
codex --model gpt-5.4 "task"             # 命令行标志
codex -c model=custom-model-name "task"  # key=value 覆盖
# 交互式 /model 命令切换
```

**ds2api 适配要点**：不应对模型名做硬编码匹配，应直接将 `model` 字段透传给底层翻译层，或在 `promptcompat` 中做 model-alias 映射。

---

## 6. 推理（Reasoning）支持

### 6.1 请求端

Codex CLI 对所有 o 系列和 GPT-5.x 推理模型发送 `reasoning` 字段：

```json
{
  "reasoning": {
    "effort": "medium",   // 可选值：minimal | low | medium | high | xhigh
    "summary": "auto"     // "auto" | "detailed" | "none"
  }
}
```

`xhigh` 是 2026 年新增的超高努力级别，模型依赖，不通用。

### 6.2 响应端

推理模型会在流中发出推理摘要事件（`response.reasoning_summary_text.delta`），以及在输出中包含 `reasoning` 类型的 output item。推理内容的加密版本可通过 `include: ["reasoning.encrypted_content"]` 请求获取，供会话续接使用。

**ds2api 适配**：`reasoning` 字段目前在转发至 DeepSeek 后端时应视为提示词增强依据（可转换为思维链提示），或直接忽略不报错。响应中若无 `reasoning_summary_text` 事件，Codex CLI **不会崩溃**，只是不显示推理摘要。

---

## 7. 上下文管理与会话续接

### 7.1 两种续接模式

Codex CLI 使用 Responses API 的两种上下文延续模式，可根据配置选择：

**模式 A：输入数组链（无状态）**  
每轮请求携带完整的 `input` 历史数组，将前一轮的可见输出与工具调用追加进去，不使用 `previous_response_id`。provider-owned opaque reasoning/compaction item 只能由原提供方解释，ds2api 会跳过这些状态而不是将密文写入提示词。

**模式 B：`previous_response_id` 链**  
仅在 `input` 中传入新用户消息，通过 `previous_response_id` 引用前一轮响应 ID，历史由服务端维护。此模式依赖 `store: true`。

### 7.2 自动上下文压缩（Compaction）

OpenAI 原生服务可在 `context_management.compact_threshold` 设置后（如 0.85）生成 provider-owned compaction state。ds2api 将该字段解释为本次请求的提前压缩预算：按当前模型的动态有效输入上限乘以阈值，调用已有的本地历史压缩流程。当前 Codex 的 v2 `compaction_trigger` 也会被识别，并返回一个本代理可解析的本地 compaction handle；该 handle 不是 OpenAI provider-owned ciphertext。

独立压缩端点：`POST /v1/responses/compact`，发送完整上下文，返回压缩后的上下文窗口供下轮使用。

**ds2api 适配**：
- `previous_response_id`：ds2api 在进程内按调用方保存 canonical input，并追加上一响应的可见 output；过期或不存在的 ID 返回 404。provider-owned encrypted reasoning/compaction 状态不会被伪造。
- 自动 compaction：本地自动压缩启用且 `compact_threshold` 为 `(0,1)` 时，按模型的动态有效输入上限计算本次压缩预算；非法值被容忍且不报 400/500。`compaction_trigger` 的 SSE 响应只发出一个已完成的 `compaction` output item，再发 `response.completed`。
- `/v1/responses/compact` 端点：返回 `{ "output": [{ "type": "compaction", "encrypted_content": "ds2api_compact_..." }] }`。该值是按调用方隔离、受 Responses store TTL 约束的本地不透明句柄；进程重启、TTL 过期或换调用方后不可使用。它不是可迁移或可由 OpenAI 验证的密文。

### 7.3 增量上游会话复用

除 `previous_response_id` 的本地 Responses 状态外，ds2api 在
`auto_delete.mode=none` 时还会复用同一 DeepSeek 上游 session。第二轮必须
是上一轮 canonical input + 可见 output 的严格前缀扩展；命中后请求只携带
本轮新增 input，以及每轮都会重新发送的强制输出格式提示词和工具约束，且
设置上轮的 `parent_message_id`。分支被编辑、设置/工具契约变化、会话过期或
并发占用都会走完整历史重放。固定父消息调用失败时，本轮立即新建 session
并完整重放，不会切换到另一个账号继续使用旧父消息。

该缓存是进程内六小时 TTL、每个作用域最多八条分支，重启后丢失。它让连续多轮
请求能够利用上游已保留的长上下文，但不改变模型单次输入上限，也不是 OpenAI
provider-owned 的可迁移状态。

---

## 8. 已知与代理不兼容问题

以下问题来自 LiteLLM、OpenRouter 等代理服务与 Codex CLI 的社区反馈，对 ds2api 有直接参考价值：

### 8.1 端点路由错误（`chat/completions` vs `responses`）

**现象**：Codex 发送 `POST /v1/responses`，代理将其路由到 `POST /v1/chat/completions`，导致：

```
400 Bad Request: "This model is only supported in v1/responses and not in v1/chat/completions"
```

**根因**：`wire_api` 未正确设为 `"responses"`，或代理在路由时未区分两个端点。

**ds2api 修复位置**：`internal/httpapi/openai/responses/handler.go` — 确保该路由独立于 chat handler，不被统一代理到 chat 流程。

### 8.2 工具定义格式错误

**现象**：代理将 Responses API 工具定义（无 `function` 嵌套）转换成 Chat Completions 格式再转发，导致 `tools[0].function` 缺失报错，或反向转换时结构异常。

**根因**：Responses API 工具定义字段在**顶层**（`name`, `description`, `parameters`），Chat Completions 要求嵌套在 `function` 子对象内。

**ds2api 修复位置**：`internal/promptcompat/` 中的 `tool_prompt.go` 或工具 schema 规范化逻辑 — 在将 Responses API 工具列表转换为后端提示词时，需处理顶层格式，不应假设有 `function` 子对象。

### 8.3 推理字段被代理拒绝

**现象**：代理收到 `reasoning: {effort: "medium"}` 后不认识该字段，返回 `400 Unknown parameter`。

**解决方案**：LiteLLM 建议配置 `drop_params: true`；ds2api 应对不认识的顶层字段采用**静默忽略**策略，而非报错。

**ds2api 修复位置**：`internal/promptcompat/standard_request.go` — 解析 Responses API 请求体时跳过未知字段，不做严格 schema 验证。

### 8.4 `previous_response_id` 找不到

**现象**：Codex 携带 `previous_response_id` 续接上轮对话，代理/后端不存储响应，返回 404 或忽略该字段重新开始对话，造成上下文丢失。

**解决方案**：ds2api 必须实现响应对象存储（已有 `response_store.go`），并能通过 ID 查询重建 `input` 历史数组。

**ds2api 修复位置**：`internal/httpapi/openai/responses/response_store.go`

### 8.5 `compaction` 类型的 input item 报错

**现象**：当 Codex 将含 `type: "compaction"` 的 item 放入 `input` 数组时，后端不认识该类型，拒绝请求。

**解决方案**：ds2api 的 input 规范化逻辑应对未知 `type` 的 item 保持宽容——可将其降级为文本内容或静默跳过，不报错。

**ds2api 修复位置**：`internal/promptcompat/responses_input_items.go` — 在 `normalizeResponsesInputItemWithState` 中增加 `case "compaction":` 的处理分支（提取 `content` 或 `encrypted_content` 作为占位文本，或直接跳过）。

### 8.6 MCP 工具的 `outputSchema` 字段未知

**现象**：MCP 工具定义包含非标准的 `outputSchema` 元数据字段，某些代理在工具 schema 校验时拒绝。

**解决方案**：工具定义解析时对未知额外字段保持宽容，只提取 `name`/`description`/`parameters` 用于提示词构造，其余字段忽略。

**ds2api 修复位置**：`internal/toolcall/toolcalls_schema_normalize.go`

### 8.7 ID 格式校验过严

**现象**（LiteLLM issue #14991）：当使用 GPT-5 系列模型时，代理返回的响应 ID 格式与 OpenAI 原始格式不符（如包含非法字符或长度异常），导致 Codex 在发送 `previous_response_id` 时被上游拒绝为"malformed ID"。

**解决方案**：ds2api 生成响应 ID 时应使用标准 UUID v4 或 `resp_` + 随机十六进制的形式，与 OpenAI 官方格式保持一致。

**ds2api 修复位置**：`internal/httpapi/openai/responses/responses_handler.go`（响应对象构造处）

---

## ds2api 版本影响说明

| ds2api 版本 | 对 Codex CLI 的影响 |
|------------|-------------------|
| **v1.0.8** | `/v1/responses` handler 现在接入 `AutoDeleteRemoteSession` deferred cleanup。若 WebUI 开启"结束后全部删除"，Codex 发起的 Responses API 请求结束后会正确清理远程会话。 |
| **v1.0.10** | 严格模型 allowlist 启用。若 Codex 配置的模型 id（如 `gpt-5.4`、`gpt-5.4-mini`）不在 `DefaultModelAliases` 中，返回 4xx。`DefaultModelAliases` 已预置约 100 个 OpenAI 常见 id；若有自定义 id，需在 WebUI Settings → Model Aliases 添加映射（热重载，无需重启）。 |
| **v1.0.12** | 上游 429 触发账号切换（不消耗重试配额）。Codex 在峰值时段发送密集请求时，账号池故障转移对客户端透明，`response.failed` 率显著降低。 |

## ds2api 适配清单

以下列出需要确认或修改的具体项目，按优先级排序：

### P0（Codex 基本功能所依赖）

| 编号 | 检查项 | 涉及文件 | 状态 |
|------|--------|---------|------|
| C-01 | `POST /v1/responses` 路由独立存在，不代理到 chat/completions 流程 | `internal/httpapi/openai/responses/handler.go` | 确认 |
| C-02 | Responses API 工具定义顶层格式（无 `function` 嵌套）被正确解析 | `internal/promptcompat/tool_prompt.go` | 确认 |
| C-03 | `instructions` 字段被转换为系统消息注入到对话首位 | `internal/promptcompat/responses_input_normalize.go` | 已实现 |
| C-04 | `input` 数组中的 `function_call` / `function_call_output` 类型被正确映射为对话历史 | `internal/promptcompat/responses_input_items.go` | 已实现 |
| C-05 | 流式响应使用 `event: <type>\ndata: <json>\n\n` 格式，且每个 data 含 `sequence_number` | `internal/httpapi/openai/responses/responses_stream_runtime_events.go` | 已实现 |
| C-06 | 流末尾发送 `data: [DONE]\n\n`（无 `event:` 前缀） | `internal/httpapi/openai/responses/responses_stream_runtime_events.go` | 已实现 |
| C-07 | `usage` 字段包含在 `response.completed` 事件中 | `internal/format/openai/render_stream_events.go` | 确认 |
| C-08 | 响应对象 ID 格式为 `resp_` + UUID 或同等格式，无特殊字符 | `internal/httpapi/openai/responses/responses_handler.go` | 确认 |
| C-09-auto | `/v1/responses` handler deferred cleanup 调用 `AutoDeleteRemoteSession`（WebUI 会话删除策略生效） | responses handler | ✅ **v1.0.8 已修复** |

### P1（多轮对话与上下文续接）

| 编号 | 检查项 | 涉及文件 | 状态 |
|------|--------|---------|------|
| C-09 | `previous_response_id` 字段被识别，能查询存储的响应并重建 `input` 历史 | `internal/httpapi/openai/responses/previous_response.go` | 确认（进程内、按调用方、TTL 内） |
| C-10 | `context_management.compact_threshold` 为 `(0,1)` 时触发本地提前压缩预算，非法值被容忍 | `internal/httpapi/openai/shared/dynamic_prompt_limit.go` | 确认（本地句柄，不伪造 provider state） |
| C-11 | `compaction` / `context_compaction` / `compaction_trigger` 输入状态不会泄漏为提示词；本代理句柄在 TTL 内可展开 | `internal/httpapi/openai/responses/compact.go` | 确认 |
| C-12 | `reasoning` 字段（`effort`, `summary`）被静默忽略或转换为思维链提示，不报 400 | `internal/promptcompat/thinking_injection.go` | 确认 |

### P2（工具与模型适配）

| 编号 | 检查项 | 涉及文件 | 状态 |
|------|--------|---------|------|
| C-13 | 工具定义中未知额外字段（如 `outputSchema`）被忽略，不影响 schema 规范化 | `internal/toolcall/toolcalls_schema_normalize.go` | 确认 |
| C-14 | `parallel_tool_calls: true` 字段被容忍（不报错，行为上串行执行亦可） | `internal/httpapi/openai/shared/handler_toolcall_policy.go` | 确认 |
| C-15 | `model` 字段透传给翻译层，不做硬编码模型名校验 | `internal/promptcompat/standard_request.go` | 确认 |
| C-16 | `POST /v1/responses/compact` 返回单个本地 opaque compaction item；Codex v2 trigger 流返回一个完成 item 后结束 | `internal/httpapi/openai/responses/compact.go` | 确认（仅进程内 TTL 状态） |
| C-17 | `include: ["reasoning.encrypted_content"]` 等 `include` 数组字段被容忍 | `internal/promptcompat/standard_request.go` | 确认 |

---

## 附录：测试验证命令

```bash
# 1. 验证基础单轮请求（无工具）
OPENAI_BASE_URL=http://localhost:8080/v1 \
OPENAI_API_KEY=test \
codex exec "echo hello world"

# 2. 验证工具调用（shell 工具）
OPENAI_BASE_URL=http://localhost:8080/v1 \
OPENAI_API_KEY=test \
codex exec "list the files in current directory"

# 3. 验证自定义 provider 配置
# ~/.codex/config.toml:
# [model_providers.local]
# base_url = "http://localhost:8080/v1"
# env_key  = "OPENAI_API_KEY"
# wire_api = "responses"
# [profiles.local]
# model = "deepseek-chat"
# model_provider = "local"
codex --profile local exec "refactor this function"

# 4. 验证 previous_response_id 续接
# 使用 codex resume 命令或直接 API 调用验证
```

---

*信息来源：OpenAI 官方文档（developers.openai.com/codex）、GitHub openai/codex 仓库讨论、LiteLLM 兼容性 issue 追踪，以及 morphllm.com 的 Codex provider 配置指南。部分内部实现细节（如精确 User-Agent 字符串格式）为近似描述，正式对接时建议通过网络抓包确认。*
