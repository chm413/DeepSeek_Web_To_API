# DeepSeek Web To API

Language: [中文](README.MD) | [English](README.en.md)

DeepSeek Web To API is a self-hosted Go gateway that exposes DeepSeek Web sessions through OpenAI, Anthropic Claude, and Gemini-compatible APIs. It includes an admin console for accounts, sessions, caches, logs, and proxy routing.

Current version: **v1.1.1**

## Features

- OpenAI endpoints: `/v1/models`, `/v1/chat/completions`, `/v1/responses`, `/v1/files`, and `/v1/embeddings`.
- Anthropic endpoints: `/anthropic/v1/messages`, `/v1/messages`, `/messages`, and token counting.
- Gemini-compatible `generateContent` and `streamGenerateContent` routes.
- Managed account pool with concurrency slots, token refresh, health checks, ban/login-failure detection, automatic disable, and manual enable/disable controls.
- Context handling with dynamic input-limit detection, Compact workflows, incremental sessions, mandatory output-format instructions, conversation rotation, and history compression.
- Request logs covering processed context size, token usage, cache hit rate, per-account cost, and conversation history.
- SOCKS5/SOCKS5H, VLESS, VMess, and Hysteria2/HY2 routing with subscriptions, scheduled updates, batch tests, automatic disable, and fallback routes.

## Shared Xray Architecture

The application does not start one Xray process per account or per node. All Xray nodes currently referenced by enabled account routes are written into one configuration and hosted by one shared Xray process. Each active node receives one loopback-only SOCKS inbound.

```text
Account A ─┐                 ┌─ inbound A -> VLESS node
Account B ─┼─ Go gateway ─── shared Xray process
Account C ─┘                 └─ inbound B -> HY2 node
```

- Multiple accounts using one node share one route.
- Nodes not referenced by enabled accounts do not remain in the resident Xray configuration.
- Manual and batch tests temporarily add routes to the shared process, then restore the account route set.
- Nodes are disabled after the configured consecutive-failure threshold. Traffic uses the configured fallback node, or a direct connection when no fallback is available.
- Missing Xray binaries can be downloaded from official XTLS/Xray-core releases into `data/xray`. In Docker this persists under `/app/data/xray`.

See [Xray and proxy subscriptions](docs/xray-proxy.md).

## Quick Start

### Windows

```powershell
Copy-Item .env.example .env
npm ci --prefix webui
npm run build --prefix webui
go run ./cmd/DeepSeek_Web_To_API
```

The admin console is available at `http://127.0.0.1:5001/admin` by default. Change the API keys, admin credentials/JWT secret, and bind address before deployment.

### Docker Compose

```powershell
Copy-Item .env.example .env
docker compose up -d
docker compose ps
```

Compose maps host port `6011` to container port `5001` by default. Override it with `DEEPSEEK_WEB_TO_API_HOST_PORT`. The `./data` volume stores configuration writeback, SQLite files, caches, logs, and Xray. The image is `ghcr.io/chm413/deepseek-web-to-api:latest` and includes a `/healthz` container health check.

### Local Build

```powershell
npm ci --prefix webui
npm run build --prefix webui
$version = (Get-Content VERSION -Raw).Trim()
go build -trimpath -ldflags "-s -w -X DeepSeek_Web_To_API/internal/version.BuildVersion=$version" -o deepseek-web-to-api.exe ./cmd/DeepSeek_Web_To_API
```

## Proxy Subscriptions and Health Policy

The Proxy page in the admin console supports:

- Airport subscription URLs entered through a password field and never returned by safe admin APIs.
- Plain URI lists, base64 URI lists, and Clash YAML/JSON proxy lists.
- Manual or scheduled subscription refresh, with optional node testing after each update.
- Single-node tests, batch tests, and batch enable, disable, and delete operations.
- Configurable test interval, concurrency, consecutive-failure threshold, recovery enablement, and global fallback route.
- Persistent latency, HTTP status, consecutive failures, last error, subscription ownership, and disable reason.

The management endpoints are under `/admin/proxies/*` and require administrator authentication.

## Configuration and Security

Start with [.env.example](.env.example) and [config.example.json](config.example.json). Secrets include account passwords and tokens, proxy credentials, node URIs, subscription URLs, API keys, and admin credentials.

- Safe admin read APIs never return account passwords, proxy passwords, node URIs, or subscription URLs.
- Full configuration exports contain secrets and must be handled as sensitive files.
- Docker deployments must keep `/app/data` writable so configuration, SQLite databases, and Xray downloads persist.
- Use an HTTPS reverse proxy and restrict access to the admin console for internet-facing deployments.

## Development and Releases

```powershell
go test ./...
npm run build --prefix webui
bash ./scripts/lint.sh
```

GitHub Actions runs Go, WebUI, cross-platform, and Docker build gates on pushes and pull requests. A `VERSION` change on `main`, or a version tag, builds Windows/Linux/macOS archives, checksums, `linux/amd64` and `linux/arm64` images, and creates or updates the GitHub Release.

## Documentation

- [Deployment](docs/deployment.md)
- [Configuration](docs/configuration.md)
- [Xray and proxy subscriptions](docs/xray-proxy.md)
- [Batch account API](docs/account-batch-api.md)
- [Account health checks](docs/account-health.md)
- [Client compatibility](docs/client-compat/README.md)
- [Testing and delivery](docs/Testing%20and%20Delivery/Testing%20and%20Delivery.md)

## License

[MIT](LICENSE)
