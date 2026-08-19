#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT_DIR"

source "${ROOT_DIR}/scripts/release-targets.sh"

resolve_release_tag() {
  local tag="${RELEASE_TAG:-}" version_file

  if [[ -n "$tag" ]]; then
    printf '%s\n' "$tag"
    return 0
  fi
  if [[ ! -f VERSION ]]; then
    echo "release tag is empty; set RELEASE_TAG or provide VERSION." >&2
    return 1
  fi

  version_file="$(tr -d '[:space:]' < VERSION)"
  if [[ -z "$version_file" ]]; then
    echo "VERSION is empty; set RELEASE_TAG or provide a release version." >&2
    return 1
  fi

  # GitHub Releases and the self-update asset resolver use the tag verbatim.
  # The release workflow derives v<version> from VERSION, so manual archive
  # builds must do the same or their files cannot be found after upload.
  printf 'v%s\n' "${version_file#v}"
}

build_one() {
  local tag="$1" build_version="$2" goos="$3" goarch="$4" goarm="$5" label="$6"
  local pkg stage bin

  pkg="deepseek-web-to-api_${tag}_${label}"
  stage="dist/${pkg}"
  bin="deepseek-web-to-api"
  if [[ "$goos" == "windows" ]]; then
    bin="deepseek-web-to-api.exe"
  fi

  echo "[release-archives] building ${label}"
  rm -rf "$stage"
  mkdir -p "${stage}/static"

  if [[ "$goarm" == "-" ]]; then
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
      go build -buildvcs=false -trimpath -ldflags="-s -w -X DeepSeek_Web_To_API/internal/version.BuildVersion=${build_version}" -o "${stage}/${bin}" ./cmd/DeepSeek_Web_To_API
  else
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" GOARM="$goarm" \
      go build -buildvcs=false -trimpath -ldflags="-s -w -X DeepSeek_Web_To_API/internal/version.BuildVersion=${build_version}" -o "${stage}/${bin}" ./cmd/DeepSeek_Web_To_API
  fi

  cp config.example.json .env.example LICENSE README.MD README.en.md "${stage}/"
  cp -R static/admin "${stage}/static/admin"

  if [[ "$goos" == "windows" ]]; then
    (cd dist && zip -rq "${pkg}.zip" "${pkg}")
  else
    tar -C dist -czf "dist/${pkg}.tar.gz" "${pkg}"
  fi

  rm -rf "$stage"
}

if [[ "${1:-}" == "--build-one" ]]; then
  shift
  build_one "$@"
  exit 0
fi

if [[ "${1:-}" == "--print-resolved-tag" ]]; then
  resolve_release_tag
  exit $?
fi

tag="$(resolve_release_tag)"

build_version="${BUILD_VERSION:-$tag}"
jobs="${RELEASE_BUILD_JOBS:-}"
if [[ -z "$jobs" ]]; then
  if command -v nproc >/dev/null 2>&1; then
    jobs="$(nproc)"
  elif command -v sysctl >/dev/null 2>&1; then
    jobs="$(sysctl -n hw.ncpu)"
  else
    jobs="2"
  fi
fi

mkdir -p dist

if [[ "$jobs" -le 1 ]]; then
  for target in "${DEEPSEEK_WEB_TO_API_RELEASE_TARGETS[@]}"; do
    read -r goos goarch goarm label <<< "$target"
    build_one "$tag" "$build_version" "$goos" "$goarch" "$goarm" "$label"
  done
else
  printf '%s\n' "${DEEPSEEK_WEB_TO_API_RELEASE_TARGETS[@]}" \
    | xargs -L 1 -P "$jobs" bash "${ROOT_DIR}/scripts/build-release-archives.sh" --build-one "$tag" "$build_version"
fi
