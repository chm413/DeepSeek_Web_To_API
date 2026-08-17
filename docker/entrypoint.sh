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

release_is_runnable() {
  release_tag="$1"
  release_dir="${self_update_root}/versions/${release_tag}"
  [ -x "${release_dir}/deepseek-web-to-api" ] && [ -d "${release_dir}/static/admin" ]
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

clear_pending_candidate() {
  rm -f \
    "${self_update_root}/pending.version" \
    "${self_update_root}/pending.previous.version" \
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
      mark_failed_candidate "${pending_tag}" || true
      clear_pending_candidate
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
    mark_failed_candidate "${pending_tag}" || true
    clear_pending_candidate
    printf '%s\n' "[self-update] pending candidate is not runnable; retaining previous release" >&2
  fi

  if [ "${selected_binary}" = "${immutable_binary}" ]; then
    current_tag="$(read_marker "${self_update_root}/current.version" 2>/dev/null || true)"
    immutable_tag="$(read_marker "${immutable_version_file}" 2>/dev/null || true)"
    if [ -n "${current_tag}" ] && release_is_runnable "${current_tag}" && { [ -z "${immutable_tag}" ] || tag_is_newer "${current_tag}" "${immutable_tag}"; }; then
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
    mark_failed_candidate "${pending_tag}" || true
    clear_pending_candidate
    printf '%s\n' "[self-update] pending candidate exited before readiness; falling back to the previous release" >&2
    continue
  fi

  exit "${exit_code}"
done
