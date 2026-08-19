#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT_DIR"

expected="v$(tr -d '[:space:]' < VERSION)"
actual="$(bash ./scripts/build-release-archives.sh --print-resolved-tag)"
[[ "$actual" == "$expected" ]] || {
  echo "default resolved tag: got $actual, want $expected" >&2
  exit 1
}

actual="$(RELEASE_TAG="release-override" bash ./scripts/build-release-archives.sh --print-resolved-tag)"
[[ "$actual" == "release-override" ]] || {
  echo "explicit resolved tag: got $actual, want release-override" >&2
  exit 1
}
