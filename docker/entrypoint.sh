#!/bin/sh
# Keep self-updated releases outside the immutable image. The Go process uses
# exit code 75 after staging a candidate. The candidate is only promoted after
# it has bound its listening socket; a failed first boot falls back to the
# previous current version in the same launcher process.
set -u

# The override variables are used only by the repository's Linux smoke test.
# Normal containers use the immutable paths baked into the image.
immutable_binary="${DEEPSEEK_WEB_TO_API_SELF_UPDATE_IMMUTABLE_BINARY:-/usr/local/bin/deepseek-web-to-api}"
immutable_version_file="${DEEPSEEK_WEB_TO_API_SELF_UPDATE_IMMUTABLE_VERSION_FILE:-/usr/local/share/deepseek-web-to-api/VERSION}"
self_update_root="${DEEPSEEK_WEB_TO_API_SELF_UPDATE_ROOT:-/app/data/self-update}"
restart_exit_code="75"
child_pid=""

# Expose the Docker update contract to the application without allowing a
# container configuration typo to change the supervisor's restart code.
export DEEPSEEK_WEB_TO_API_SELF_UPDATE_ROOT="${self_update_root}"
export DEEPSEEK_WEB_TO_API_SELF_UPDATE_CONTAINER="true"
export DEEPSEEK_WEB_TO_API_SELF_UPDATE_RESTART_EXIT_CODE="${restart_exit_code}"

# Docker's inherited CMD names the immutable binary. The wrapper chooses the
# actual binary itself, so omit that default argument while preserving user
# supplied arguments such as --config or --debug.
if [ "$#" -eq 1 ] && [ "$1" = "${immutable_binary}" ]; then
  set --
fi

if [ "${DEEPSEEK_WEB_TO_API_STATIC_ADMIN_DIR+x}" = "x" ]; then
  configured_static_admin_dir="${DEEPSEEK_WEB_TO_API_STATIC_ADMIN_DIR}"
  static_admin_dir_was_set="true"
else
  configured_static_admin_dir=""
  static_admin_dir_was_set="false"
fi

restore_configured_static_admin_dir() {
  if [ "${static_admin_dir_was_set}" = "true" ]; then
    export DEEPSEEK_WEB_TO_API_STATIC_ADMIN_DIR="${configured_static_admin_dir}"
  else
    unset DEEPSEEK_WEB_TO_API_STATIC_ADMIN_DIR
  fi
}

read_marker() {
  marker_path="$1"
  if [ -r "${marker_path}" ]; then
    IFS= read -r marker_value < "${marker_path}" || true
    case "${marker_value}" in
      "" | *[!v0123456789.]* | *..* | v. | *. ) return 1 ;;
      v*.*.* )
        printf '%s' "${marker_value}"
        return 0
        ;;
      * ) return 1 ;;
    esac
  fi
  return 1
}

hash_file() {
  hash_output=""
  if command -v sha256sum >/dev/null 2>&1; then
    hash_output="$(sha256sum -- "$1")" || return 1
  elif [ -x /usr/local/bin/busybox ]; then
    hash_output="$(/usr/local/bin/busybox sha256sum "$1")" || return 1
  else
    return 1
  fi
  printf '%s\n' "${hash_output%% *}"
}

metadata_value() {
  metadata_key="$1"
  metadata_path="$2"
  case "${metadata_key}" in
    tag)
      sed -n 's/.*"tag":"\([^"]*\)".*/\1/p' "${metadata_path}" | head -n 1
      ;;
    binary_sha256)
      sed -n 's/.*"binary_sha256":"\([^"]*\)".*/\1/p' "${metadata_path}" | head -n 1
      ;;
    static_tree_sha256)
      sed -n 's/.*"static_tree_sha256":"\([^"]*\)".*/\1/p' "${metadata_path}" | head -n 1
      ;;
    *)
      return 1
      ;;
  esac
}

is_sha256() {
  hash_value="$1"
  [ "${#hash_value}" -eq 64 ] || return 1
  case "${hash_value}" in
    *[!0123456789abcdefABCDEF]* | "") return 1 ;;
  esac
  return 0
}

static_tree_hash() {
  static_dir="$1"
  tree_input="${self_update_root}/.static-tree.$$"
  [ -d "${static_dir}" ] || return 1
  # Symlinks are rejected even when they point inside the candidate tree.
  if find "${static_dir}" -type l -print -quit 2>/dev/null | grep . >/dev/null 2>&1; then
    return 1
  fi
  (umask 077 && : > "${tree_input}") || return 1
  if ! find "${static_dir}" -type f -print 2>/dev/null | sort | while IFS= read -r file; do
    rel=${file#"${static_dir}"/}
    printf '%s\000' "${rel}" >> "${tree_input}" || exit 1
    file_hash="$(hash_file "${file}" 2>/dev/null || true)"
    [ "${#file_hash}" -eq 64 ] || exit 1
    printf '%s\000' "${file_hash}" >> "${tree_input}" || exit 1
  done; then
    rm -f "${tree_input}"
    return 1
  fi
  if [ ! -s "${tree_input}" ]; then
    rm -f "${tree_input}"
    return 1
  fi
  tree_hash="$(hash_file "${tree_input}" 2>/dev/null || true)"
  rm -f "${tree_input}"
  is_sha256 "${tree_hash}" || return 1
  printf '%s\n' "${tree_hash}"
}

release_integrity_is_valid() {
  release_tag="$1"
  release_dir="${self_update_root}/versions/${release_tag}"
  metadata_path="${release_dir}/.verified.json"
  [ -f "${metadata_path}" ] && [ ! -L "${metadata_path}" ] || return 1
  declared_tag="$(metadata_value tag "${metadata_path}" 2>/dev/null || true)"
  [ "${declared_tag}" = "${release_tag}" ] || return 1
  declared_binary_hash="$(metadata_value binary_sha256 "${metadata_path}" 2>/dev/null || true)"
  is_sha256 "${declared_binary_hash}" || return 1
  actual_binary_hash="$(hash_file "${release_dir}/deepseek-web-to-api" 2>/dev/null || true)"
  [ "${actual_binary_hash}" = "${declared_binary_hash}" ] || return 1
  declared_static_hash="$(metadata_value static_tree_sha256 "${metadata_path}" 2>/dev/null || true)"
  is_sha256 "${declared_static_hash}" || return 1
  actual_static_hash="$(static_tree_hash "${release_dir}/static/admin" 2>/dev/null || true)"
  [ "${actual_static_hash}" = "${declared_static_hash}" ] || return 1
  return 0
}

release_is_runnable() {
  release_tag="$1"
  release_dir="${self_update_root}/versions/${release_tag}"
  [ -x "${release_dir}/deepseek-web-to-api" ] && [ ! -L "${release_dir}/deepseek-web-to-api" ] && \
    [ -d "${release_dir}/static/admin" ] && [ ! -L "${release_dir}/static/admin" ] && \
    release_integrity_is_valid "${release_tag}"
}

tag_is_newer() {
  left_body="${1#v}"
  right_body="${2#v}"
  original_ifs="${IFS}"
  IFS=.
  set -- ${left_body}
  left_major="${1:-}"; left_minor="${2:-}"; left_patch="${3:-}"
  left_count="$#"
  set -- ${right_body}
  right_major="${1:-}"; right_minor="${2:-}"; right_patch="${3:-}"
  right_count="$#"
  IFS="${original_ifs}"
  if [ "${left_count}" -ne 3 ] || [ "${right_count}" -ne 3 ]; then
    return 1
  fi
  for number in "${left_major}" "${left_minor}" "${left_patch}" "${right_major}" "${right_minor}" "${right_patch}"; do
    case "${number}" in "" | *[!0123456789]*) return 1 ;; esac
  done
  for pair in "${left_major}:${right_major}" "${left_minor}:${right_minor}" "${left_patch}:${right_patch}"; do
    left_number="${pair%%:*}"
    right_number="${pair#*:}"
    if [ "${left_number}" -gt "${right_number}" ]; then
      return 0
    fi
    if [ "${left_number}" -lt "${right_number}" ]; then
      return 1
    fi
  done
  return 1
}

mark_pending_attempt() {
  attempt_path="${self_update_root}/pending.attempted"
  temp_path="${attempt_path}.$$"
  (umask 077 && printf '%s\n' "$1" > "${temp_path}") || return 1
  mv -f "${temp_path}" "${attempt_path}"
}

mark_failed_candidate() {
  failed_path="${self_update_root}/failed.version"
  temp_path="${failed_path}.$$"
  (umask 077 && printf '%s\n' "$1" > "${temp_path}") || return 1
  mv -f "${temp_path}" "${failed_path}"
}

write_version_marker() {
  marker_path="$1"
  marker_value="$2"
  temp_path="${marker_path}.$$"
  (umask 077 && printf '%s\n' "${marker_value}" > "${temp_path}") || return 1
  mv -f "${temp_path}" "${marker_path}"
}

# Recover a candidate confirmation which died between marker writes. Newer
# candidates persist both the previously-running version and the old rollback
# pointer; older transactions simply fall back to their untouched current
# marker. This is intentionally best-effort: an unwritable persistent volume
# cannot be repaired by the entrypoint, but it must never start the candidate
# again in this invocation.
restore_pending_release_state() {
  pending_previous="$(read_marker "${self_update_root}/pending.previous.version" 2>/dev/null || true)"
  if [ -n "${pending_previous}" ]; then
    write_version_marker "${self_update_root}/current.version" "${pending_previous}" || return 1
  fi

  rollback_path="${self_update_root}/pending.rollback.previous.version"
  if [ ! -r "${rollback_path}" ]; then
    return 0
  fi
  IFS= read -r rollback_value < "${rollback_path}" || true
  case "${rollback_value}" in
    none)
      rm -f "${self_update_root}/previous.version"
      ;;
    *)
      rollback_previous="$(read_marker "${rollback_path}" 2>/dev/null || true)"
      if [ -z "${rollback_previous}" ]; then
        return 1
      fi
      write_version_marker "${self_update_root}/previous.version" "${rollback_previous}"
      ;;
  esac
}

recover_failed_candidate() {
  candidate_tag="$1"
  mark_failed_candidate "${candidate_tag}" || true
  if ! restore_pending_release_state; then
    printf '%s\n' "[self-update] failed to fully restore pending release state" >&2
  fi
  clear_pending_candidate
}

clear_pending_candidate() {
  rm -f \
    "${self_update_root}/pending.version" \
    "${self_update_root}/pending.previous.version" \
    "${self_update_root}/pending.rollback.previous.version" \
    "${self_update_root}/pending.attempted"
}

forward_signal() {
  signal_name="$1"
  if [ -n "${child_pid}" ]; then
    kill -s "${signal_name}" "${child_pid}" 2>/dev/null || true
    wait "${child_pid}" 2>/dev/null || true
  fi
  exit 0
}

trap 'forward_signal TERM' TERM
trap 'forward_signal INT' INT
trap 'forward_signal HUP' HUP

while :; do
  selected_binary="${immutable_binary}"
  started_pending="false"
  restore_configured_static_admin_dir
  unset DEEPSEEK_WEB_TO_API_SELF_UPDATE_ACTIVE_VERSION

  pending_tag="$(read_marker "${self_update_root}/pending.version" 2>/dev/null || true)"
  if [ -n "${pending_tag}" ] && release_is_runnable "${pending_tag}"; then
    attempt_path="${self_update_root}/pending.attempted"
    if [ -f "${attempt_path}" ]; then
      recover_failed_candidate "${pending_tag}"
      printf '%s\n' "[self-update] pending candidate failed before readiness; retaining previous release" >&2
    elif mark_pending_attempt "${pending_tag}"; then
      release_dir="${self_update_root}/versions/${pending_tag}"
      selected_binary="${release_dir}/deepseek-web-to-api"
      export DEEPSEEK_WEB_TO_API_STATIC_ADMIN_DIR="${release_dir}/static/admin"
      export DEEPSEEK_WEB_TO_API_SELF_UPDATE_ACTIVE_VERSION="${pending_tag}"
      started_pending="true"
      printf '%s\n' "[self-update] starting pending release: ${pending_tag}" >&2
    fi
  elif [ -n "${pending_tag}" ]; then
    recover_failed_candidate "${pending_tag}"
    printf '%s\n' "[self-update] pending candidate is not runnable; retaining previous release" >&2
  fi

  if [ "${selected_binary}" = "${immutable_binary}" ]; then
    current_tag="$(read_marker "${self_update_root}/current.version" 2>/dev/null || true)"
    failed_tag="$(read_marker "${self_update_root}/failed.version" 2>/dev/null || true)"
    immutable_tag="$(read_marker "${immutable_version_file}" 2>/dev/null || true)"
    if [ -n "${current_tag}" ] && [ "${current_tag}" = "${failed_tag}" ]; then
      printf '%s\n' "[self-update] quarantined persistent release will not be started: ${current_tag}" >&2
      printf '%s\n' "[self-update] starting immutable image release: ${immutable_tag:-unknown}" >&2
    elif [ -n "${current_tag}" ] && ! release_is_runnable "${current_tag}"; then
      # A pending candidate can restore current.version to the immutable
      # image's tag. That tag is intentionally not present in the persistent
      # versions directory, so it is not a tampered candidate and must not
      # overwrite failed.version while being used as the fallback.
      if [ -n "${immutable_tag}" ] && [ "${current_tag}" = "${immutable_tag}" ]; then
        rm -f "${self_update_root}/current.version"
        printf '%s\n' "[self-update] persistent marker matches immutable image; using image release: ${immutable_tag}" >&2
      else
        mark_failed_candidate "${current_tag}" || true
        printf '%s\n' "[self-update] persistent release failed integrity validation; quarantining: ${current_tag}" >&2
      fi
      printf '%s\n' "[self-update] starting immutable image release: ${immutable_tag:-unknown}" >&2
    elif [ -n "${current_tag}" ] && release_is_runnable "${current_tag}" && { [ -z "${immutable_tag}" ] || tag_is_newer "${current_tag}" "${immutable_tag}"; }; then
      release_dir="${self_update_root}/versions/${current_tag}"
      selected_binary="${release_dir}/deepseek-web-to-api"
      export DEEPSEEK_WEB_TO_API_STATIC_ADMIN_DIR="${release_dir}/static/admin"
      export DEEPSEEK_WEB_TO_API_SELF_UPDATE_ACTIVE_VERSION="${current_tag}"
      printf '%s\n' "[self-update] starting persistent release: ${current_tag}" >&2
    else
      printf '%s\n' "[self-update] starting immutable image release: ${immutable_tag:-unknown}" >&2
    fi
  fi

  "${selected_binary}" "$@" &
  child_pid="$!"
  wait "${child_pid}"
  exit_code="$?"
  child_pid=""

  if [ "${exit_code}" -eq "${restart_exit_code}" ]; then
    printf '%s\n' "[self-update] update requested restart; reloading persistent release slot" >&2
    continue
  fi

  # A first-boot candidate which exits before it promotes itself leaves the
  # pending marker intact. Fall back in this same entrypoint process so the
  # recovery does not depend on Docker's restart policy being configured.
  pending_after_exit="$(read_marker "${self_update_root}/pending.version" 2>/dev/null || true)"
  if [ "${started_pending}" = "true" ] && [ "${pending_after_exit}" = "${pending_tag}" ] && [ -f "${self_update_root}/pending.attempted" ]; then
    recover_failed_candidate "${pending_tag}"
    printf '%s\n' "[self-update] pending candidate exited before readiness; falling back to the previous release" >&2
    continue
  fi

  exit "${exit_code}"
done
