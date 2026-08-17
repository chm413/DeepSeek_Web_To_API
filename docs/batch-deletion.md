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

Proxy deletion is blocked with HTTP `409 Conflict` when a selected proxy is the
configured fallback route, or when its assigned accounts do not have a safe
replacement. The response includes a `references` array for fallback conflicts.
If the deletion can proceed, it is atomic and returns `route_changes`:

- manually routed accounts move to the configured, enabled fallback proxy;
- automatically routed accounts move to a remaining, enabled node whose latest
  test succeeded, using the normal least-assigned routing rule;
- every moved enabled account has its saved Token cleared and is signed in again
  through its new exit.

The operation is rejected before changing configuration if there is no enabled
fallback for a manual route, automatic routing is disabled, or no tested
replacement is available. It never silently converts either route type to direct
egress. The Web UI describes the planned migration in its confirmation dialog
and reports how many account routes moved after success.
