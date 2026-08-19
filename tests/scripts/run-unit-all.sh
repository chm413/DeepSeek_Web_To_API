#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT_DIR"

./tests/scripts/run-unit-go.sh
./tests/scripts/run-unit-node.sh
bash ./tests/scripts/test-self-update-entrypoint.sh
bash ./tests/scripts/test-legacy-config-migration.sh
bash ./tests/scripts/test-release-archive-tag.sh
