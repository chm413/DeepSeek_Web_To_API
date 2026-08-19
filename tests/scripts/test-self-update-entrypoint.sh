#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
ENTRYPOINT="$ROOT_DIR/docker/entrypoint.sh"

if [[ ! -f "$ENTRYPOINT" ]]; then
  echo "missing Docker self-update entrypoint: $ENTRYPOINT" >&2
  exit 1
fi

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT
UPDATE_ROOT="$TMP_DIR/self-update"
LOG_FILE="$TMP_DIR/launches.log"
IMMUTABLE_BIN="$TMP_DIR/immutable"
IMMUTABLE_VERSION="$TMP_DIR/immutable.version"

make_binary() {
  local path="$1" label="$2" exit_code="$3"
  cat > "$path" <<EOF
#!/bin/sh
printf '%s\\n' '$label' >> '$LOG_FILE'
exit $exit_code
EOF
  chmod +x "$path"
}

make_release() {
  local tag="$1" label="$2" exit_code="$3"
  local dir="$UPDATE_ROOT/versions/$tag"
  mkdir -p "$dir/static/admin"
  make_binary "$dir/deepseek-web-to-api" "$label" "$exit_code"
  printf '%s\n' "$label" > "$dir/static/admin/index.html"
  local binary_sha256
  binary_sha256="$(sha256sum "$dir/deepseek-web-to-api" | awk '{print $1}')"
  local static_file_hash static_tree_sha256
  static_file_hash="$(sha256sum "$dir/static/admin/index.html" | awk '{print $1}')"
  static_tree_sha256="$(printf 'index.html\000%s\000' "$static_file_hash" | sha256sum | awk '{print $1}')"
  printf '{"tag":"%s","binary_sha256":"%s","static_tree_sha256":"%s"}\n' "$tag" "$binary_sha256" "$static_tree_sha256" > "$dir/.verified.json"
}

run_entrypoint() {
  DEEPSEEK_WEB_TO_API_SELF_UPDATE_ROOT="$UPDATE_ROOT" \
  DEEPSEEK_WEB_TO_API_SELF_UPDATE_IMMUTABLE_BINARY="$IMMUTABLE_BIN" \
  DEEPSEEK_WEB_TO_API_SELF_UPDATE_IMMUTABLE_VERSION_FILE="$IMMUTABLE_VERSION" \
  bash "$ENTRYPOINT"
}

printf 'v1.2.0\n' > "$IMMUTABLE_VERSION"
make_binary "$IMMUTABLE_BIN" "immutable" 0
make_release "v1.1.9" "older-persistent" 0
printf 'v1.1.9\n' > "$UPDATE_ROOT/current.version"
run_entrypoint

if [[ "$(cat "$LOG_FILE")" != "immutable" ]]; then
  echo "newer immutable image must win over older persistent release" >&2
  exit 1
fi

: > "$LOG_FILE"
make_release "v1.3.0" "broken-candidate" 1
printf 'v1.3.0\n' > "$UPDATE_ROOT/pending.version"
printf 'v1.2.0\n' > "$UPDATE_ROOT/pending.previous.version"
run_entrypoint

expected=$'broken-candidate\nimmutable'
if [[ "$(cat "$LOG_FILE")" != "$expected" ]]; then
  echo "failed candidate must fall back to the immutable release in one launcher run" >&2
  cat "$LOG_FILE" >&2
  exit 1
fi
if [[ "$(cat "$UPDATE_ROOT/failed.version")" != "v1.3.0" ]]; then
  echo "failed candidate tag was not quarantined" >&2
  exit 1
fi
if [[ -e "$UPDATE_ROOT/pending.version" || -e "$UPDATE_ROOT/pending.previous.version" || -e "$UPDATE_ROOT/pending.rollback.previous.version" || -e "$UPDATE_ROOT/pending.attempted" ]]; then
  echo "failed candidate markers were not cleared" >&2
  exit 1
fi

: > "$LOG_FILE"
# Simulate a process that died after it had written current.version and
# previous.version but before it removed pending.version. The entrypoint must
# restore both pointers from the pending transaction before choosing a fallback.
printf 'v1.0.0\n' > "$IMMUTABLE_VERSION"
make_release "v1.2.0" "restored-persistent" 0
make_release "v1.3.1" "partially-promoted-candidate" 1
printf 'v1.3.1\n' > "$UPDATE_ROOT/current.version"
printf 'v1.2.0\n' > "$UPDATE_ROOT/previous.version"
printf 'v1.3.1\n' > "$UPDATE_ROOT/pending.version"
printf 'v1.2.0\n' > "$UPDATE_ROOT/pending.previous.version"
printf 'v1.1.9\n' > "$UPDATE_ROOT/pending.rollback.previous.version"
run_entrypoint

expected=$'partially-promoted-candidate\nrestored-persistent'
if [[ "$(cat "$LOG_FILE")" != "$expected" ]]; then
  echo "partially promoted candidate must restore and fall back in one launcher run" >&2
  cat "$LOG_FILE" >&2
  exit 1
fi
if [[ "$(cat "$UPDATE_ROOT/current.version")" != "v1.2.0" || "$(cat "$UPDATE_ROOT/previous.version")" != "v1.1.9" ]]; then
  echo "partial candidate recovery did not restore version pointers" >&2
  exit 1
fi
if [[ -e "$UPDATE_ROOT/pending.rollback.previous.version" ]]; then
  echo "pending rollback snapshot was not cleared after recovery" >&2
  exit 1
fi

: > "$LOG_FILE"
# A failed marker is also a startup quarantine. This prevents an interrupted
# recovery from repeatedly selecting a failed tag that still occupies
# current.version.
make_release "v1.5.0" "quarantined-persistent" 1
printf 'v1.5.0\n' > "$UPDATE_ROOT/current.version"
printf 'v1.5.0\n' > "$UPDATE_ROOT/failed.version"
run_entrypoint
if [[ "$(cat "$LOG_FILE")" != "immutable" ]]; then
  echo "quarantined persistent release must not be selected again" >&2
  cat "$LOG_FILE" >&2
  exit 1
fi

: > "$LOG_FILE"
make_release "v1.4.0" "newer-persistent" 0
printf 'v1.4.0\n' > "$UPDATE_ROOT/current.version"
run_entrypoint
if [[ "$(cat "$LOG_FILE")" != "newer-persistent" ]]; then
  echo "newer persistent release must win over immutable image" >&2
  exit 1
fi

: > "$LOG_FILE"
# A persistent release whose binary changed after staging must be quarantined
# before it is ever executed. The immutable image remains the fallback.
printf 'tampered\n' >> "$UPDATE_ROOT/versions/v1.4.0/deepseek-web-to-api"
printf 'v1.4.0\n' > "$UPDATE_ROOT/current.version"
run_entrypoint
if [[ "$(cat "$LOG_FILE")" != "immutable" ]]; then
  echo "tampered persistent release must not be executed" >&2
  cat "$LOG_FILE" >&2
  exit 1
fi

: > "$LOG_FILE"
# Static assets are part of the verified release contract too.
make_release "v1.6.0" "tampered-static" 0
printf 'tampered-static\n' >> "$UPDATE_ROOT/versions/v1.6.0/static/admin/index.html"
printf 'v1.6.0\n' > "$UPDATE_ROOT/current.version"
run_entrypoint
if [[ "$(cat "$LOG_FILE")" != "immutable" ]]; then
  echo "tampered static assets must not be executed" >&2
  cat "$LOG_FILE" >&2
  exit 1
fi

echo "self-update-entrypoint=ok"
