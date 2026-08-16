# Upstream Account Health

The account health path uses direct HTTP responses. Browser rendering is not
part of the runtime implementation.

## Status source

Password login calls:

```text
POST /api/v0/users/login
```

The login response is the primary status source. The relevant fields are:

- `data.biz_code=10` (`USER_IS_BANNED`): explicit account ban.
- `data.biz_data.user.status=0`: normal account status observed after login.
- `data.biz_data.user.chat.is_muted=1`: upstream temporary mute.
- `data.biz_data.user.chat.mute_until`: Unix seconds for the mute expiry.

After a successful login, the same status can be refreshed with:

```text
GET /api/v0/users/current
```

Set `runtime.account_health_check_interval_minutes` to a value from 1 to 1440
to poll this endpoint in the background. The default is `0`, which disables
scheduled polling. The configured monitor is started with the server, so
changing this setting requires a restart; real-time response inspection does
not require the monitor.

The upstream response code `40012` means `USER_IS_BANNED`; `50006` means
`MUTED`. These codes are treated as account health signals only when they are
returned by an authenticated upstream account request.

Completion responses are also inspected while the SSE body is read, so a
`50006` event inside an HTTP 200 stream is handled the same way as an HTTP
error response. The stream bytes are passed through unchanged.

## Local handling

- `temporarily_muted`: the account is removed from pool rotation until
  `mute_until`. If no expiry is supplied, the frontend-compatible fallback is
  seven days. Session affinity bindings for the account are forgotten.
- `permanently_banned`: the account is persistently marked `disabled=true`
  with `disabled_reason=upstream_banned`, removed from rotation immediately,
  and must be manually re-enabled after the upstream account is restored.
- `healthy`: a fresh password login clears an old temporary or permanent
  in-memory state.
- `disabled`: an operator manually disabled the account. Disabled accounts are
  rejected by both normal rotation and explicit target-account requests.

Temporary mute expiry and the latest detailed health reason remain
process-local. Permanent automatic disable state is stored with the account in
the configured accounts SQLite database or JSON account list, so quarantine
survives process restarts. Passwords, proxy credentials, tokens, and upstream
response bodies are not written to health logs.

The admin account list returns `enabled`, `disabled`, `disabled_reason`,
`disabled_at`, and `account_state`. Accounts can be enabled or disabled from
the account table or edit dialog. Re-enabling clears the local health
quarantine and returns the account to the pool.

## Signals that are not bans

- Ordinary HTTP 429 is a temporary account throughput/quota signal. It enters
  a bounded cooldown and can route to another healthy managed account; it is
  never a persistent disable by itself.
- A 429 that explicitly says the current conversation reached its context or
  turn/message capacity is a session-only signal. It keeps the account
  available, rotates the conversation on the same account, and is exposed as
  structured `413` / `upstream_session_capacity` if the rebuilt prompt still
  cannot fit.
- An empty completion is a request/model retry signal. It is not sufficient to
  quarantine an account, especially for oversized Pro prompts.
- HTTP 413 is a local or model-context limit and must be handled by prompt
  limiting/compression.
- HTTP 403 from the local safety or IP policy is not an account ban.
- HTTP 5xx, timeout, and proxy failures are transport failures.
