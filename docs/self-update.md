# Docker Self-Update

The Docker self-update feature lets a running container switch application
files without replacing its image. It is intentionally limited to release
archives published by this repository's GitHub Release workflow.

The first image that supports this feature must be deployed normally. Later
updates only write the persistent Compose `./data` volume.

## Version Source And Release Build

`VERSION` is the single release-version source. Push a matching stable tag,
for example `v1.2.2`, to start `release-artifacts.yml`. The workflow checks
that the tag and `VERSION` agree, runs the release gates, then publishes:

- Linux archive assets for `linux/amd64` and `linux/arm64`.
- `sha256sums.txt` containing the exact asset digests.
- Multi-architecture GHCR image tags.

Normal pushes and pull requests continue to run `quality-gates.yml`, including
the Go, Web UI, Docker, and cross-build checks. A normal branch push never
creates a GitHub Release by itself.

## Runtime Flow

1. The server periodically queries the GitHub latest-release endpoint.
2. It accepts stable semantic-version releases only, then selects the exact
   Linux archive for the running CPU architecture.
3. Before extraction it downloads the same-release `sha256sums.txt` and
   verifies the archive SHA-256.
4. It rejects archives over 256 MiB, traversal paths, links, special files,
   and missing binary or Web UI assets.
5. Verified files are staged in `/app/data/self-update/versions/<tag>/`.
6. Applying an update writes a pending marker and exits with code `75` after
   the HTTP response is sent. The image entrypoint starts that candidate.
7. The candidate promotes itself only after it has bound the HTTP listener.
   If it fails before readiness, the entrypoint keeps the old current version,
   restores the old rollback pointer, records the failed tag, and falls back
   in the same launcher process.
   Automatic apply skips that tag until an administrator explicitly retries it.
   The launcher also refuses to select a quarantined tag from the persistent
   release slot, even if an interrupted confirmation left it in `current.version`.

The launcher never overwrites `/usr/local/bin/deepseek-web-to-api`, does not
touch configuration or SQLite files, and does not require a Docker socket.
It compares the immutable image version with the persistent override and
starts the newer one, so pulling a newer image is never hidden by an older
downloaded release.

## Policy

The `app_update` config block is persisted in `config.json` and can be edited
in the Web UI under Settings > Application Updates.

```json
{
  "app_update": {
    "enabled": true,
    "auto_download": false,
    "auto_apply": false,
    "check_interval_minutes": 360
  }
}
```

Automatic checks default to enabled every 360 minutes. Automatic download and
automatic apply default to disabled. Applying always restarts the application
process, so it can interrupt active streams; manual download followed by a
scheduled maintenance-window apply is the conservative production policy.

The management API is administrator-authenticated:

| Method | Endpoint | Purpose |
| --- | --- | --- |
| `GET` | `/admin/updates` | Current version, latest release, runtime state, and policy. |
| `POST` | `/admin/updates/check` | Check the latest release now. |
| `POST` | `/admin/updates/download` | Download and verify the latest release. |
| `POST` | `/admin/updates/apply` | Mark the verified release pending and restart. |
| `POST` | `/admin/updates/rollback` | Return to the previous staged version or immutable image. |
| `PUT` | `/admin/updates/settings` | Update the policy fields directly. |

An installation endpoint only works in a container launched by the bundled
entrypoint. Non-Docker processes can still inspect release status but cannot
overwrite their own executable.

## Storage Layout

```text
/app/data/self-update/
  current.version
  previous.version
  pending.version
  pending.previous.version
  pending.rollback.previous.version
  failed.version
  versions/
    v1.2.2/
      deepseek-web-to-api
      static/admin/
      .verified.json
```

The marker files contain only a stable version tag. `.verified.json` records
the staged archive name and SHA-256, never account, proxy, API, or admin
credentials.

## Trust Boundary

SHA-256 validation protects against download corruption and accidental asset
mismatches. The archive and checksum file are both GitHub Release assets, so
it does not by itself protect against compromise of the release publisher.
For a stronger publisher-authentication guarantee, sign `sha256sums.txt` with
a pinned public-key scheme such as minisign or cosign in a future release.
