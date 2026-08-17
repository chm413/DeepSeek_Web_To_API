# DeepSeek Web To API

语言 / Language: [中文](README.MD) | [English](README.en.md)

DeepSeek Web To API 是一个自托管 Go 网关，将 DeepSeek Web 会话转换为 OpenAI、Anthropic Claude 和 Gemini 兼容接口，并提供账号、会话、缓存、日志和代理路由管理台。

当前版本：**v1.2.1**

## 主要能力

- OpenAI：`/v1/models`、`/v1/chat/completions`、`/v1/responses`、`/v1/files`、`/v1/embeddings`。
- Anthropic：`/anthropic/v1/messages`、`/v1/messages`、`/messages` 和 token 计数接口。
- Gemini：`generateContent` 与 `streamGenerateContent` 兼容入口。
- 多账号池：并发槽位、Token 刷新、账号健康检测、封禁/登录失败识别、自动禁用和手动启停。
- 上下文处理：输入限制识别、Compact 协议、增量会话、严格输出格式提示、会话轮换与历史压缩。
- 可观测性：请求日志、内部处理后上下文大小、Token 使用量、缓存命中率、账号成本和会话历史。
- 代理路由：SOCKS5/SOCKS5H、Shadowsocks（`ss://`）、VLESS、VMess、Hysteria2/HY2，支持机场订阅、定时更新、批量测试、自动禁用、粘性自动路由和兜底节点。

## Xray 路由架构

应用不会为每个账号或每个节点启动一个 Xray 进程。所有被启用账号实际引用的 Xray 节点统一写入一份配置，由一个共享 Xray 进程承载；每个活动节点只分配一个仅监听 `127.0.0.1` 的本地 SOCKS 入站。

```text
Account A ─┐                 ┌─ inbound A -> VLESS node
Account B ─┼─ Go gateway ─── Xray shared process
Account C ─┘                 └─ inbound B -> HY2 node
```

- 多个账号使用同一节点时只保留一条路由。
- 未被启用账号引用的节点不会常驻 Xray 配置。
- 单节点或批量测试临时加入共享进程，测试后恢复账号路由集合。
- 自动路由池只包含最新检测成功的节点；账号按启用账号分配数最少优先，首次分配或当前节点失效时才切换，并在切换后重新登录获取 Token。
- 手动路由的节点连续失败达到阈值后自动禁用；已配置兜底节点时切换到兜底，否则直连。自动路由账号无健康节点时阻断请求，不静默泄漏到直连。
- Xray 缺失时可从官方 XTLS/Xray-core Release 自动下载到 `data/xray`。Docker 中该目录位于持久化卷 `/app/data/xray`。

详见 [Xray 与代理订阅](docs/xray-proxy.md)。

## 快速开始

### Windows

```powershell
Copy-Item .env.example .env
npm ci --prefix webui
npm run build --prefix webui
go run ./cmd/DeepSeek_Web_To_API
```

默认管理台地址是 `http://127.0.0.1:5001/admin`。首次使用前应修改 `.env` 中的 API Key、管理密码/JWT 密钥和监听地址。

### Docker Compose

```powershell
Copy-Item .env.example .env
docker compose up -d
docker compose ps
```

默认映射 `6011:5001`，可用 `DEEPSEEK_WEB_TO_API_HOST_PORT` 修改宿主机端口。`./data` 保存配置回写、SQLite、缓存、日志和 Xray 核心。镜像为 `ghcr.io/chm413/deepseek-web-to-api:latest`，内置 `/healthz` 健康检查。

### 本地构建

```powershell
npm ci --prefix webui
npm run build --prefix webui
$version = (Get-Content VERSION -Raw).Trim()
go build -trimpath -ldflags "-s -w -X DeepSeek_Web_To_API/internal/version.BuildVersion=$version" -o deepseek-web-to-api.exe ./cmd/DeepSeek_Web_To_API
```

## 代理订阅与健康策略

管理台的“代理 IP”页面支持：

- 添加机场订阅，订阅 URL 使用密码输入框且安全管理接口不回显。
- 解析普通 URI 列表、base64 URI 列表以及 Clash YAML/JSON 节点，包括 Shadowsocks `ss://` 链接和 Clash `type: ss`/`shadowsocks` 节点。
- 手动或定时更新订阅，并选择更新后是否自动测试节点。
- 单节点测试、批量测试、批量启用、禁用和删除。
- 配置测试间隔、并发数、连续失败阈值、恢复后自动启用和全局兜底节点。
- 可选粘性自动路由；健康节点按已启用账号分配数最少优先，临时封禁账号仍计入分配数，避免不必要地改变出口 IP。
- 查看可用性、延迟、出口 IP、地区、HTTP 状态、已分配账号数、连续失败次数、最后错误、订阅归属和禁用原因。

Shadowsocks 插件传输不受此 Xray 集成支持；含 URI `plugin` 参数或 Clash `plugin`/`plugin-opts` 的节点会被拒绝。

相关管理接口位于 `/admin/proxies/*`，需要管理员认证。

## 配置与安全

推荐从 [.env.example](.env.example) 和 [config.example.json](config.example.json) 开始。敏感字段包括账号密码、Token、代理密码、节点 URI、订阅 URL、API Key 和管理员凭据。

- 安全管理读取接口不返回账号密码、代理密码、节点 URI 或订阅 URL。
- 完整配置导出包含秘密，只应保存到可信位置。
- Docker 部署应保持 `/app/data` 可写，否则配置、SQLite 和 Xray 下载无法持久化。
- 对外提供服务时应使用 HTTPS 反向代理，并限制管理端入口。

## 开发与发布

```powershell
go test ./...
npm run build --prefix webui
bash ./scripts/lint.sh
```

根目录的 `VERSION` 是发布版本源。推送与其一致的版本标签（如 `v1.2.1`）会自动运行发布门禁，构建 Windows/Linux/macOS 发布包、校验和及 `linux/amd64`、`linux/arm64` GHCR 镜像，并创建或更新对应的 GitHub Release。也可从 Actions 手动触发发布；标签和 `VERSION` 不一致时流水线会明确失败，避免错误版本进入更新通道。

## 文档

- [部署](docs/deployment.md)
- [Docker 容器内自更新](docs/self-update.md)
- [旧版本配置与数据迁移](docs/data-migration.md)
- [配置](docs/configuration.md)
- [Xray 与代理订阅](docs/xray-proxy.md)
- [账号批量导入 API](docs/account-batch-api.md)
- [批量删除](docs/batch-deletion.md)
- [账号健康检测](docs/account-health.md)
- [客户端兼容](docs/client-compat/README.md)
- [测试与交付](docs/Testing%20and%20Delivery/Testing%20and%20Delivery.md)

## License

[MIT](LICENSE)
