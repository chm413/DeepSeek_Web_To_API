# DeepSeek_Web_To_API

Language: [中文](README.MD) | [English](README.en.md)

DeepSeek_Web_To_API is a self-hosted Go gateway that exposes DeepSeek Web sessions through OpenAI-, Claude-, and Gemini-compatible APIs. It also includes a React/Vite admin console for accounts, keys, proxies, response cache, chat history, and runtime metrics.

Current version: **v1.0.13** · Self-hosted, see [docs/deployment.md](docs/deployment.md)

## Highlights

- **OpenAI-compatible routes**: `/v1/models`, `/v1/chat/completions`, `/v1/responses`, `/v1/files`, `/v1/embeddings`.
- **Claude-compatible routes**: `/anthropic/v1/messages`, `/v1/messages`, `/messages`, and `count_tokens`.
- **Gemini-compatible routes**: `/v1beta/models/{model}:generateContent`, `streamGenerateContent`, and `/v1/models/{model}:*`.
- **Managed account pool** with token refresh, queueing, per-account concurrency, and optional target-account routing. Setting `globalMaxInflight=1` with multiple accounts now emits a startup WARN (Issue #19 footgun guard).
- **429 elastic fail-over**: upstream 429 triggers an account switch without consuming the `maxAttempts` budget as long as the pool has untried accounts — transparent to the caller. 401/502/5xx keep legacy behavior.
- **Direct-token mode** when the caller token is not configured as a managed API key.
- **Response cache**: memory TTL default 30 min (cap 3.8 GB), disk TTL default 48 h (cap 16 GB, gzip). TTL is 100% governed by WebUI/Store config; hot-reload takes effect immediately — no restart needed.
- **CIF prefix reuse (Current Input File)**: inline-prefix mode (no file upload required) reuses stable conversation context across turns and accounts; up to 2 prefix variants per session (LRU promote); `maxTailChars` 128 KB; `chat_history` schema carries 7 `cif_*` columns; WebUI exposes 4 metric cards (PREFIX reuse rate / CHECKPOINT refreshes / TAIL size / CURRENT INPUT latency).
- **Thinking-injection prompt split**: `ReasoningEffortPrompt` (~250 B) appended to the latest user message tail; `ToolChainPlaybookPrompt` (~3 KB) prepended to the system message head via `PrependPlaybookToSystem` — eliminates upstream fast-path silently dropping playbook rules.
- **Strict model allowlist**: `resolveCanonicalModel` no longer falls back to heuristic family-prefix matching; unknown model IDs return 4xx instead of routing silently to a default. `deepseek-v4-vision` is removed from `/v1/models` and blocked at every internal resolution path (alias targets included).
- **Unified session auto-delete**: `AutoDeleteRemoteSession` shared helper is wired to all four LLM paths — `/v1/chat/completions`, `/v1/responses`, `/v1/messages` (Anthropic/Claude Code), and Gemini (via `proxyViaOpenAI`). The WebUI "auto-delete sessions" toggle is now honored everywhere.
- **Hot-reloadable safety policy**: banned_content / banned_regex / jailbreak patterns / blocked IPs / auto-ban, all stored in independent SQLite databases. PUT `/admin/settings` applies changes immediately at runtime. Production policy: ~140 banned_content + 35 banned_regex + 151 jailbreak patterns + 6 blocked IPs + auto-ban (threshold=3 / window=600 s).
- **Correct version reporting**: `/admin/version` reads `internal/version.BuildVersion` injected via `-ldflags` at build time; `scripts/deploy_107.py` does this automatically, ending the "dev" mis-report on manual deploys.
- **Operations**: `/healthz`, `/readyz`, security response headers, CORS, JSON UTF-8 inbound validation, graceful shutdown.
- **SQLite chat history** with gzip-compressed detail blobs and a default 20,000-record retention limit.
- **Admin console** served from `/admin`.
- **Extended proxy protocols** through a local Xray core: VLESS, VMess, and Hysteria2/HY2 node URIs, with core status/version reporting and secret-safe admin responses. See [Xray proxy protocols](docs/xray-proxy.md).

## Quick Start

```bash
cp .env.example .env
npm ci --prefix webui
npm run build --prefix webui
go run ./cmd/DeepSeek_Web_To_API
```

Then open `http://127.0.0.1:5001/admin`.

Docker Compose:

```bash
cp .env.example .env
docker compose up -d
```

Binary build with version injection:

```bash
npm ci --prefix webui
npm run build --prefix webui
go build -trimpath \
  -ldflags="-s -w -X DeepSeek_Web_To_API/internal/version.BuildVersion=$(cat VERSION)" \
  -o deepseek-web-to-api ./cmd/DeepSeek_Web_To_API
```

`scripts/deploy_107.py` automates cross-compilation for `linux/amd64`, version injection, SCP upload, sha256 verification, and `systemd` restart. Set `SKIP_BUILD=1` to skip the build step.

## Required Configuration

Edit the `DEEPSEEK_WEB_TO_API_CONFIG_JSON` value in `.env`:

- `api_keys`: API keys used by client applications.
- `admin.key` or `admin.password_hash`: admin login credential.
- `admin.jwt_secret`: JWT signing secret.
- `server.bind_addr`: use `127.0.0.1` when Caddy/Nginx terminates public traffic.

Use `.env` only for deployment-layer overrides such as Docker host port, config path, or platform-injected config JSON. Accounts are imported via the admin console bulk-import page — no JSON editing required.

## Documentation

- [Documentation Index (Chinese)](docs/README.md)
- [Project Overview](docs/Project%20Overview/Project%20Overview.md)
- [Architecture Design](docs/Architecture%20Design/Architecture%20Design.md)
- [API Reference](API.en.md)
- [Configuration](docs/configuration.md)
- [Deployment](docs/deployment.md)
- [Storage and Cache](docs/storage-cache.md)
- [Security](docs/security.md)
- [Prompt Compatibility](docs/prompt-compatibility.md)
- [Testing and Delivery](docs/Testing%20and%20Delivery/Testing%20and%20Delivery.md)

## Local Gates

```bash
./scripts/lint.sh
./tests/scripts/check-refactor-line-gate.sh
./tests/scripts/run-unit-all.sh
npm run build --prefix webui
```

## License

This project is released under the [MIT License](LICENSE). Anyone may use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of the software, subject to inclusion of the copyright and license notice. The software is provided "AS IS", without warranty of any kind.

## Disclaimer

This project is intended for learning, research, personal experiments, and internal validation. Users are responsible for complying with applicable service terms, platform rules, and laws.
