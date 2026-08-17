#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
MIGRATOR="$ROOT_DIR/docker/migrate-legacy-config.sh"

if [[ ! -f "$MIGRATOR" ]]; then
  echo "missing legacy config migration helper: $MIGRATOR" >&2
  exit 1
fi

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT
LEGACY_DIR="$TMP_DIR/legacy"
DATA_DIR="$TMP_DIR/data"
LEGACY_CONFIG="$LEGACY_DIR/config.json"
TARGET_CONFIG="$DATA_DIR/config.json"
mkdir -p "$LEGACY_DIR"

run_migrator() {
  DEEPSEEK_WEB_TO_API_LEGACY_CONFIG_SOURCE="$LEGACY_CONFIG" \
  DEEPSEEK_WEB_TO_API_CONFIG_PATH="$TARGET_CONFIG" \
  sh "$MIGRATOR"
}

printf '%s\n' '{"legacy":true,"name":"first"}' > "$LEGACY_CONFIG"
run_migrator

if ! cmp -s "$LEGACY_CONFIG" "$TARGET_CONFIG"; then
  echo "legacy config was not copied exactly" >&2
  exit 1
fi
if find "$DATA_DIR" -maxdepth 1 -name '.config.json.migrate.*' -print -quit | grep -q .; then
  echo "migration left a temporary config behind" >&2
  exit 1
fi

# Existing persistent configuration is authoritative, even if the old source
# later changes. This is the important no-overwrite property of upgrades.
printf '%s\n' '{"legacy":true,"name":"changed"}' > "$LEGACY_CONFIG"
printf '%s\n' '{"persistent":true}' > "$TARGET_CONFIG"
run_migrator
if [[ "$(cat "$TARGET_CONFIG")" != '{"persistent":true}' ]]; then
  echo "migration overwrote an existing persistent config" >&2
  exit 1
fi

# A new install has no old root config and must remain a successful no-op.
rm -f "$LEGACY_CONFIG" "$TARGET_CONFIG"
run_migrator
if [[ -e "$TARGET_CONFIG" ]]; then
  echo "migration created a config without a legacy source" >&2
  exit 1
fi

# A non-file target is likewise an existing target and must be left intact.
mkdir "$TARGET_CONFIG"
printf '%s\n' '{"legacy":true,"name":"directory-target"}' > "$LEGACY_CONFIG"
run_migrator
if [[ ! -d "$TARGET_CONFIG" ]]; then
  echo "migration changed a non-file target" >&2
  exit 1
fi

# Two init containers can race during an operator retry. The published target
# must contain one complete source and the losing process must not replace it.
RACE_DIR="$TMP_DIR/race-data"
RACE_TARGET="$RACE_DIR/config.json"
RACE_SOURCE_ONE="$TMP_DIR/race-one.json"
RACE_SOURCE_TWO="$TMP_DIR/race-two.json"
printf '%s\n' '{"legacy":"one"}' > "$RACE_SOURCE_ONE"
printf '%s\n' '{"legacy":"two"}' > "$RACE_SOURCE_TWO"
DEEPSEEK_WEB_TO_API_LEGACY_CONFIG_SOURCE="$RACE_SOURCE_ONE" \
DEEPSEEK_WEB_TO_API_CONFIG_PATH="$RACE_TARGET" \
sh "$MIGRATOR" >"$TMP_DIR/race-one.log" 2>&1 &
race_one_pid=$!
DEEPSEEK_WEB_TO_API_LEGACY_CONFIG_SOURCE="$RACE_SOURCE_TWO" \
DEEPSEEK_WEB_TO_API_CONFIG_PATH="$RACE_TARGET" \
sh "$MIGRATOR" >"$TMP_DIR/race-two.log" 2>&1 &
race_two_pid=$!
wait "$race_one_pid"
wait "$race_two_pid"
case "$(cat "$RACE_TARGET")" in
  '{"legacy":"one"}'|'{"legacy":"two"}') ;;
  *)
    echo "concurrent migration did not publish one complete source" >&2
    exit 1
    ;;
esac
if find "$RACE_DIR" -maxdepth 1 -name '.config.json.migrate.*' -print -quit | grep -q .; then
  echo "concurrent migration left a temporary config behind" >&2
  exit 1
fi

echo "legacy-config-migration=ok"
