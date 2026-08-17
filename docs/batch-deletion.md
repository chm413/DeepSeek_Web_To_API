# Batch deletion

Batch deletion is available through the existing selected-item action APIs.
Both endpoints require administrator authentication and `Content-Type:
application/json`.

## Accounts

`POST /admin/accounts/batch/actions`

```json
{
  "identifiers": ["user-1@example.com", "+8613800000000"],
  "action": "delete"
}
```

Identifiers are normalized and deduplicated. The operation is atomic: if any
selected account no longer exists, no account is removed. A successful response
contains `affected` and `total_accounts`. Account credentials are never echoed
in the response. The configured account store, including `accounts.sqlite` when
enabled, is updated in the same store transaction and the request pool is reset.

## Proxies

`POST /admin/proxies/actions`

```json
{
  "proxy_ids": ["proxy-main", "proxy-backup"],
  "action": "delete"
}
```

Proxy deletion is intentionally blocked with HTTP `409 Conflict` when a selected
proxy is referenced by an account or is the configured fallback route. The
response includes a `references` array with the proxy ID, account count, a
capped account identifier preview, and the fallback flag. No routes are silently
changed; reassign the affected accounts and choose another fallback (or direct
connection) before retrying. If one selected proxy is blocked, the full batch is
left unchanged.

The Web UI mirrors these constraints: it confirms the selected-item count,
shows the reference warning before deletion, and clears account selection after
a successful account deletion.
