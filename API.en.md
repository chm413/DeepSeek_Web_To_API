# DeepSeek_Web_To_API API Reference

Language: [中文](API.md) | [English](API.en.md)

Default Base URL: `http://127.0.0.1:5001`.

## Release Notes (v1.0.8 – v1.0.12)

| Version | Summary |
|---------|---------|
| **v1.0.8** | **Auto-delete remote session unified**: `AutoDeleteRemoteSession` is now wired into the deferred cleanup of all three handler chains — `/v1/chat/completions`, `/v1/responses`, and `/v1/messages`. Previously, the Claude handler (`/v1/messages`) did not execute remote session cleanup even when the WebUI "delete all after completion" toggle was enabled (Issue #20). Claude Code users are directly affected. |
| **v1.0.10** | **Strict model allowlist (breaking change)**: The family-prefix heuristic fallback (`gpt-`, `claude-`, `gemini-`, `o1`, `o3`, `llama-`, `qwen-`, `mistral-`, `command-` prefixes silently routing to a default DeepSeek model) has been removed. Unknown model IDs now return 4xx with a strict-allowlist message. `DefaultModelAliases` covers ~100 common OpenAI/Claude/Gemini IDs. Custom IDs must be added via WebUI Settings → Model Aliases (hot-reloaded, no restart). `deepseek-v4-vision` is hidden from `/v1/models` and blocked everywhere. |
| **v1.0.12** | **429 elastic fail-over**: A 429 from upstream now triggers an account switch without consuming the retry budget. Clients see significantly fewer 429 responses during peak hours; a 429 surfaces only when every account in the pool is simultaneously rate-limited. |

## Common Rules

- JSON endpoints require valid UTF-8 request bodies.
- Health probes: `GET/HEAD /healthz`, `GET/HEAD /readyz`.
- Protocol auth: `Authorization: Bearer <token>` or `x-api-key: <token>`; Gemini also accepts `x-goog-api-key`, `?key=`, and `?api_key=`.
- Managed mode: tokens configured in `config.json` `keys` use the account pool.
- Direct-token mode: unknown tokens are passed through as DeepSeek tokens.
- Admin auth: `POST /admin/login` returns a JWT; protected admin APIs require `Authorization: Bearer <jwt>` or the admin key.
- **Model allowlist (v1.0.10+)**: `DefaultModelAliases` covers ~100 common IDs. Custom model IDs require an explicit alias in WebUI Settings → Model Aliases (hot-reloaded, no restart required).

## OpenAI-Compatible Routes

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/v1/models` | List models (`deepseek-v4-vision` hidden since v1.0.10) |
| `GET` | `/v1/models/{model_id}` | Get one model (alias-aware) |
| `POST` | `/v1/chat/completions` | Chat Completions, streaming supported; auto-delete session wired (v1.0.8) |
| `POST` | `/v1/responses` | Responses API, streaming + stored response; auto-delete session wired (v1.0.8) |
| `GET` | `/v1/responses/{response_id}` | Stored Response lookup |
| `POST` | `/v1/files` | File upload (multipart, `purpose` field supported) |
| `POST` | `/v1/embeddings` | Embeddings-compatible response (deterministic, SharedAcrossCallers cache) |

Root aliases and `/v1/v1/*` aliases are also supported.

## Claude-Compatible Routes

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/anthropic/v1/models` | Claude-style model list |
| `POST` | `/anthropic/v1/messages` | Messages, streaming supported; auto-delete session wired (v1.0.8, Issue #20 fix) |
| `POST` | `/anthropic/v1/messages/count_tokens` | Token estimate (deterministic, SharedAcrossCallers cache) |
| `POST` | `/v1/messages`, `/messages` | Messages aliases |
| `POST` | `/v1/messages/count_tokens`, `/messages/count_tokens` | Count Tokens aliases |

> **Claude Code users (v1.0.8 fix)**: Prior to v1.0.8, requests to `/v1/messages` did not trigger remote session cleanup even with the WebUI "delete all after completion" toggle enabled. This is fixed in v1.0.8 (Issue #20).

## Gemini-Compatible Routes

| Method | Path | Description |
| --- | --- | --- |
| `POST` | `/v1beta/models/{model}:generateContent` | Non-streaming generation (SharedAcrossCallers cache) |
| `POST` | `/v1beta/models/{model}:streamGenerateContent` | Streaming generation |
| `POST` | `/v1/models/{model}:generateContent` | v1 alias |
| `POST` | `/v1/models/{model}:streamGenerateContent` | v1 streaming alias |

## Admin Routes

Admin routes include login, config import/export, API keys, account pool, proxies, settings, chat history, overview metrics, and version inspection. WebUI Settings → Model Aliases allows operators to configure custom model ID mappings (hot-reloaded). See [docs/README.md](docs/README.md) for the Chinese operational documentation.

`PUT /admin/accounts/{identifier}/proxy` accepts `{"proxy_id":"node-id","auto_route":false}` for a manual route or `{"auto_route":true}` for sticky automatic routing. Automatic routing requires global `proxy_policy.auto_route_enabled=true` and a stored account password. The response includes the effective `proxy_id`, `auto_route`, `route_changed`, and `relogin`. Each item from `GET /admin/proxies` includes availability, latency, exit IP, country/colo, and assigned-account counts; its top-level `route_pool` is ordered by current assignment priority. Batch tests use `POST /admin/proxies/test-batch`, and the route policy is available through `GET`/`PUT /admin/proxies/policy`.

## Response Cache

Cacheable protocol responses may include:

- `X-DeepSeek-Web-To-API-Cache: memory|disk`
- `X-DeepSeek-Web-To-API-Cache-Expires-At: <RFC3339>`

Bypass with:

```http
Cache-Control: no-cache
X-DeepSeek-Web-To-API-Cache-Control: bypass
```
