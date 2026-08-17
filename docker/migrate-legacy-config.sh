#!/bin/sh
# Bridge the pre-data-volume Compose layout into the persistent data directory.
# This script deliberately knows nothing about application settings: it only
# copies an existing legacy config once, before the main service is allowed to
# start.
set -eu

legacy_config="${DEEPSEEK_WEB_TO_API_LEGACY_CONFIG_SOURCE:-/legacy/config.json}"
target_config="${DEEPSEEK_WEB_TO_API_CONFIG_PATH:-/app/data/config.json}"
temporary_config=""

log() {
    printf '%s\n' "[legacy-config-migration] $*" >&2
}

cleanup() {
    if [ -n "${temporary_config}" ]; then
        rm -f "${temporary_config}" || log "failed to remove temporary file: ${temporary_config}"
    fi
}

trap cleanup 0 HUP INT TERM

# A regular file, directory, or symlink at the target means another deployment
# has already made the ownership decision. Never replace it.
if [ -e "${target_config}" ] || [ -L "${target_config}" ]; then
    log "target already exists; nothing to migrate"
    exit 0
fi

if [ ! -e "${legacy_config}" ] && [ ! -L "${legacy_config}" ]; then
    log "legacy config is absent; nothing to migrate"
    exit 0
fi

if [ ! -f "${legacy_config}" ] || [ ! -r "${legacy_config}" ]; then
    log "legacy config is not a readable regular file"
    exit 1
fi

target_dir="$(dirname "${target_config}")"
if ! mkdir -p "${target_dir}"; then
    log "cannot create target directory"
    exit 1
fi
if [ ! -d "${target_dir}" ]; then
    log "target parent is not a directory"
    exit 1
fi

# Recheck after creating the directory. This keeps the no-overwrite guarantee
# even if an operator or another process creates the destination concurrently.
if [ -e "${target_config}" ] || [ -L "${target_config}" ]; then
    log "target appeared during setup; nothing to migrate"
    exit 0
fi

umask 077
temporary_config="$(mktemp "${target_dir}/.config.json.migrate.XXXXXX")"
if ! cp "${legacy_config}" "${temporary_config}"; then
    log "failed to copy legacy config into the target directory"
    exit 1
fi
if ! chmod 0600 "${temporary_config}"; then
    log "failed to protect temporary config permissions"
    exit 1
fi

# link(2) only succeeds when target_config is absent. Because the temporary
# file is created in the same directory, this publishes the complete file in
# one operation without a replace window. It also works for empty configs.
if ln "${temporary_config}" "${target_config}" 2>/dev/null; then
    if ! rm -f "${temporary_config}"; then
        log "config migrated but temporary hard link could not be removed"
        exit 1
    fi
    temporary_config=""
    log "migrated legacy config into persistent data"
    exit 0
fi

if [ -e "${target_config}" ] || [ -L "${target_config}" ]; then
    log "target appeared during publish; leaving it unchanged"
    exit 0
fi

log "failed to atomically publish legacy config"
exit 1
