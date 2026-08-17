#!/usr/bin/env bash
set -euo pipefail

# GitHub-hosted images already include Go. When their copy differs from the
# module toolchain, Go's built-in downloader retrieves the exact official
# toolchain without depending on the setup-go action archive.
required_version="${REQUIRED_GO_VERSION:-${GO_VERSION:-}}"
required_version="${required_version#go}"

if [[ ! "${required_version}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "REQUIRED_GO_VERSION must be an exact Go version such as 1.26.0; got: ${required_version:-<empty>}" >&2
  exit 1
fi

if ! command -v go >/dev/null 2>&1; then
  # macOS images keep preinstalled Go in the hosted tool cache, and the
  # Apple Silicon image may not add that directory to PATH for bash steps.
  candidate_bins=(
    "/usr/local/go/bin"
    "/opt/homebrew/bin"
    "/opt/homebrew/opt/go/libexec/bin"
    "/usr/local/opt/go/libexec/bin"
    "/Users/runner/hostedtoolcache/go"/*/*/bin
  )
  if [[ "${RUNNER_TOOL_CACHE:-}" == /* ]]; then
    candidate_bins+=("${RUNNER_TOOL_CACHE}/go"/*/*/bin)
  fi
  for candidate_bin in "${candidate_bins[@]}"; do
    if [[ -x "${candidate_bin}/go" ]]; then
      export PATH="${candidate_bin}:${PATH}"
      break
    fi
  done
fi

if ! command -v go >/dev/null 2>&1; then
  echo "The GitHub runner does not provide a bootstrap Go command." >&2
  exit 1
fi

export GOTOOLCHAIN="go${required_version}+auto"
version_output=""
for attempt in 1 2 3; do
  if version_output="$(go version 2>&1)"; then
    break
  fi

  if [[ "${attempt}" == "3" ]]; then
    echo "Unable to select Go ${required_version} after ${attempt} attempts:" >&2
    echo "${version_output}" >&2
    exit 1
  fi

  echo "Go toolchain selection failed (attempt ${attempt}/3); retrying..." >&2
  sleep "$((attempt * 2))"
done

actual_version="$(go env GOVERSION)"
if [[ "${actual_version}" != "go${required_version}" ]]; then
  echo "Expected Go go${required_version}, but resolved ${actual_version}." >&2
  exit 1
fi

if [[ -n "${GITHUB_ENV:-}" ]]; then
  printf 'GOTOOLCHAIN=%s\n' "${GOTOOLCHAIN}" >> "${GITHUB_ENV}"
fi
if [[ -n "${GITHUB_PATH:-}" ]]; then
  printf '%s\n' "$(dirname "$(command -v go)")" >> "${GITHUB_PATH}"
fi

echo "Selected ${version_output} with GOTOOLCHAIN=${GOTOOLCHAIN}"
