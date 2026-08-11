# DeepSeek_Web_To_API

语言 / Language: [中文](README.MD) | [English](README.en.md)

DeepSeek_Web_To_API 是一个自托管的 Go 网关，把 DeepSeek Web 侧的会话能力封装为 OpenAI、Claude 和 Gemini 风格的 API，并提供 React 管理台用于账号、密钥、代理、缓存、历史记录和运行状态管理。

当前版本：**v1.0.13** · 自托管部署，参见 [docs/deployment.md](docs/deployment.md)

## 核心能力

- **OpenAI 兼容**：`/v1/models`、`/v1/chat/completions`、`/v1/responses`、`/v1/files`、`/v1/embeddings`。
- **Claude 兼容**：`/anthropic/v1/messages`、`/v1/messages`、`/messages`、`count_tokens`。
- **Gemini 兼容**：`/v1beta/models/{model}:generateContent`、`streamGenerateContent` 以及 `/v1/models/{model}:*`。
- **多账号托管**：账号池自动选号、刷新 token、处理并发槽位；`globalMaxInflight=1` 多账号配置会在启动时打印 WARN 提示。
- **429 弹性 fail-over**：上游返回 429 时账号切换不消耗 maxAttempts 预算，池中仍有空闲账号时对客户端透明；401/502/5xx 保持原行为。
- **直通 token**：调用方 token 不在托管密钥中时，按 DeepSeek token 直通。
- **响应缓存**：内存默认 TTL 30 分钟（上限 3.8 GB），磁盘默认 TTL 48 小时（上限 16 GB，gzip）。TTL 由 WebUI/Store 配置全权控制，热更新立即生效。
- **CIF 前缀复用（Current Input File）**：inline-prefix 模式无需文件上传，跨账号复用历史上下文；每会话最多 2 条 prefix 变体（LRU 提升），`maxTailChars` 128 KB；chat_history 记录 7 列 CIF 状态字段；WebUI 4 张卡片可视化（PREFIX 复用率 / CHECKPOINT 刷新 / TAIL 大小 / CURRENT INPUT 耗时）。
- **Thinking-Injection 提示词拆分**：`ReasoningEffortPrompt`（~250 B）追加到 user 消息末尾，`ToolChainPlaybookPrompt`（~3 KB）前置到 system 消息头部，消除上游 fast-path 静默丢弃工具链规则的根因。
- **严格模型白名单**：`resolveCanonicalModel` 移除启发式 family-prefix fallback，未在 supported 列表或 alias map 内的模型 ID 一律返回 4xx；`deepseek-v4-vision` 从 `/v1/models` 及所有内部路径移除并硬性封锁。
- **全路径会话自动删除**：`AutoDeleteRemoteSession` 共享 helper 同时挂载到 `/v1/chat/completions`、`/v1/responses`、`/v1/messages`（Anthropic/Claude Code）三条路径；Gemini 经 proxyViaOpenAI 间接生效。
- **安全策略热加载**：banned_content / banned_regex / jailbreak.patterns / blocked_ips / 重复违规自动拉黑，全部存入独立 SQLite，运行时 PUT `/admin/settings` 热更新无需重启；生产已部署约 140 条 banned_content + 35 条 banned_regex + 151 条 jailbreak.patterns + 6 条 blocked_ips + auto_ban（threshold=3 / window=600s）。
- **版本正确上报**：`/admin/version` 通过 `internal/version.BuildVersion` ldflags 注入当前 tag，不再固定报 "dev"。
- **运维能力**：`/healthz`、`/readyz`、安全响应头、CORS、JSON UTF-8 入站校验、优雅退出。
- **账号与历史记录**：账号池单独写入 `data/accounts.sqlite`；对话历史写入 SQLite，默认保留 2 万条，详情内容 gzip 压缩。
- **管理台**：`/admin` 提供账号、代理、密钥、运行设置、对话历史、测试器、总览指标和新版本提醒。
- **扩展代理协议**：可调用本机 Xray core 使用 VLESS、VMess、Hysteria2/HY2 分享链接；核心状态、路径和运行实例数可在代理管理页查看，节点 URI 不通过安全管理接口回显。

## 快速开始

### 本地源码运行

```bash
cp .env.example .env
npm ci --prefix webui
npm run build --prefix webui
go run ./cmd/DeepSeek_Web_To_API
```

示例 `.env` 默认监听 `127.0.0.1:5001`，打开 `http://127.0.0.1:5001/admin` 进入管理台。

### Docker Compose

```bash
cp .env.example .env
docker compose up -d
```

Compose 默认把宿主机 `${DEEPSEEK_WEB_TO_API_HOST_PORT:-6011}` 映射到容器内 `5001`，并把 `./data` 挂载为配置回写、账号 SQLite、历史记录和缓存目录。

### 二进制构建

```bash
npm ci --prefix webui
npm run build --prefix webui
go build -trimpath -ldflags="-s -w -X DeepSeek_Web_To_API/internal/version.BuildVersion=$(cat VERSION)" \
  -o deepseek-web-to-api ./cmd/DeepSeek_Web_To_API
```

`scripts/deploy_107.py` 自动读取 `VERSION` 文件并注入 ldflags，SCP 上传后通过 systemd 重启，支持 `SKIP_BUILD=1` 跳过编译直接用现有产物。

## 最小配置

复制 `.env.example` 后至少修改：

- `DEEPSEEK_WEB_TO_API_CONFIG_JSON` 里的 `api_keys`：给外部客户端使用的 API Key。
- `admin.key` 或 `admin.password_hash`：管理台登录凭据。
- `admin.jwt_secret`：管理台 JWT 签名密钥。
- `server.bind_addr`：如果前面有 Caddy/Nginx 反代，建议改为 `127.0.0.1`。
- `DEEPSEEK_WEB_TO_API_ACCOUNTS_SQLITE_PATH`：账号 SQLite 路径，默认 `data/accounts.sqlite`。

账号不再需要写 JSON。进入管理台后，在"批量导入"页面直接粘贴一行一个 `账号:密码`，后端写入独立 SQLite。

## API 使用示例

OpenAI Chat Completions：

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

Claude Messages：

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

更多接口见 [API.md](API.md)。

## 文档

- [文档导航](docs/README.md)
- [项目总览](docs/Project%20Overview/Project%20Overview.md)
- [架构设计](docs/Architecture%20Design/Architecture%20Design.md)
- [配置说明](docs/configuration.md)
- [部署运维](docs/deployment.md)
- [API 兼容系统](docs/API%20Compatibility%20System/API%20Compatibility%20System.md)
- [Prompt 兼容流程](docs/prompt-compatibility.md)
- [存储与缓存](docs/storage-cache.md)
- [安全说明](docs/security.md)
- [测试与交付](docs/Testing%20and%20Delivery/Testing%20and%20Delivery.md)

## 开发验证

仓库 CI 与本地 PR Gate 使用同一批命令：

```bash
./scripts/lint.sh
./tests/scripts/check-refactor-line-gate.sh
./tests/scripts/run-unit-all.sh
npm run build --prefix webui
```

仅修改文档时，至少运行 `git diff --check` 和旧项目残留扫描。

## 许可证

本项目使用 [MIT 许可证](LICENSE)。允许任何人免费使用、复制、修改、合并、发布、分发、再许可与销售本软件的副本，前提是在所有副本或重要部分中保留版权与许可声明。软件按"原样"提供，不附带任何明示或默示的担保。

## 免责声明

本项目仅用于学习、研究、个人实验和内部验证。使用者需要自行确认部署、调用和账号使用行为是否符合相关服务条款、平台规则和法律法规。维护者不对账号风险、数据丢失、第三方限制或任何直接/间接损失负责。
