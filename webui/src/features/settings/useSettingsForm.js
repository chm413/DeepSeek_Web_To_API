import { useCallback, useEffect, useState } from 'react'

import {
    fetchSettings,
    getExportData,
    postImportData,
    postPassword,
    putSettings,
} from './settingsApi'

const DEFAULT_FORM = {
    admin: { jwt_expire_hours: 24 },
    runtime: { account_max_inflight: 2, account_max_queue: 10, global_max_inflight: 10, token_refresh_interval_hours: 6, account_health_check_interval_minutes: 0 },
    compat: { wide_input_strict_output: true, strip_reference_markers: true },
    responses: { store_ttl_seconds: 900 },
    embeddings: { provider: '' },
    cache: {
        response: {
            dir: '',
            memory_ttl_seconds: 300,
            memory_max_bytes: 3800000000,
            disk_ttl_seconds: 14400,
            disk_max_bytes: 16000000000,
            max_body_bytes: 67108864,
            semantic_key: true,
            compression: 'gzip',
        },
    },
    auto_delete: { mode: 'none' },
    current_input_file: { enabled: true, min_chars: 0 },
    thinking_injection: { enabled: true, prompt: '', default_prompt: '' },
    prompt_limit: {
        enabled: true,
        max_chars_default: 380000,
        max_chars_expert: 150000,
        max_chars_default_configured: false,
        max_chars_expert_configured: false,
        auto_compress_enabled: false,
        compress_keep_recent: 6,
        compress_keep_system: true,
        pro_flash_compression_enabled: false,
        pro_flash_compression_target_chars: 150000,
        incremental_max_turns: 0,
        incremental_rotation_keep_recent: 6,
    },
    safety: {
        enabled: false,
        block_message: '',
        blocked_ips_text: '',
        allowed_ips_text: '',
        blocked_conversation_ids_text: '',
        banned_content_text: '',
        banned_regex_text: '',
        jailbreak: { enabled: false, patterns_text: '' },
        auto_ban: { enabled: true, threshold: 3, window_seconds: 600 },
        llm_check: {
            enabled: false,
            model: 'deepseek-v4-flash-nothinking',
            timeout_ms: 5000,
            fail_open: true,
            cache_ttl_seconds: 600,
            cache_max_entries: 10000,
            min_input_chars: 30,
            max_input_chars: 8000,
            max_concurrent: 16,
        },
    },
    model_aliases_text: '{}',
}

function parseJSONMap(raw, fieldName, t) {
    const text = String(raw || '').trim()
    if (!text) {
        return {}
    }
    let parsed
    try {
        parsed = JSON.parse(text)
    } catch (_e) {
        throw new Error(t('settings.invalidJsonField', { field: fieldName }))
    }
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
        throw new Error(t('settings.invalidJsonField', { field: fieldName }))
    }
    return parsed
}

function normalizeAutoDeleteMode(raw) {
    const mode = String(raw?.mode || '').trim().toLowerCase()
    if (mode === 'none' || mode === 'single' || mode === 'all') {
        return mode
    }
    if (Boolean(raw?.sessions)) {
        return 'all'
    }
    return 'none'
}

function listToText(items) {
    if (!Array.isArray(items)) {
        return ''
    }
    return items.filter((item) => String(item || '').trim()).join('\n')
}

// textToList parses a multi-line / comma-separated textarea value into a
// clean list of trimmed entries. Each entry is sanitized to remove
// control characters (\x00-\x1F minus tab, \x7F) before being sent to
// the Go backend; without this an admin could paste a payload that
// includes embedded NULs or newlines that would later parse as separate
// list items in regex / IP / banned-content lists.
function textToList(raw) {
    return String(raw || '')
        .split(/\r?\n|,/)
        .map((item) => sanitizeListItem(item))
        .filter(Boolean)
}

function sanitizeListItem(value) {
    return String(value || '')
        // strip control chars (keep \t and \n already handled by split)
        .replace(/[\u0000-\u001f\u007f]/g, '')
        .trim()
}

// safeNumber maps any user-entered value to a finite number with explicit
// fallback. Number(e.target.value) on an empty input yields 0, on a non
// numeric string yields NaN — neither should reach the Go backend without
// a deliberate decision. min/max default to no-clamping.
function safeNumber(raw, fallback, min, max) {
    const n = Number(raw)
    if (!Number.isFinite(n)) {
        return fallback
    }
    if (typeof min === 'number' && n < min) {
        return min
    }
    if (typeof max === 'number' && n > max) {
        return max
    }
    return n
}

function fromServerForm(data) {
    const currentInputFileEnabled = data.current_input_file?.enabled ?? true
    return {
        admin: { jwt_expire_hours: Number(data.admin?.jwt_expire_hours || 24) },
        runtime: {
            account_max_inflight: Number(data.runtime?.account_max_inflight || 2),
            account_max_queue: Number(data.runtime?.account_max_queue || 10),
            global_max_inflight: Number(data.runtime?.global_max_inflight || 10),
            token_refresh_interval_hours: Number(data.runtime?.token_refresh_interval_hours || 6),
            account_health_check_interval_minutes: Number(data.runtime?.account_health_check_interval_minutes || 0),
        },
        compat: {
            wide_input_strict_output: data.compat?.wide_input_strict_output ?? true,
            strip_reference_markers: data.compat?.strip_reference_markers ?? true,
        },
        responses: {
            store_ttl_seconds: Number(data.responses?.store_ttl_seconds || 900),
        },
        embeddings: {
            provider: data.embeddings?.provider || '',
        },
        cache: {
            response: {
                dir: data.cache?.response?.dir || '',
                memory_ttl_seconds: Number(data.cache?.response?.memory_ttl_seconds || 300),
                memory_max_bytes: Number(data.cache?.response?.memory_max_bytes || 3800000000),
                disk_ttl_seconds: Number(data.cache?.response?.disk_ttl_seconds || 14400),
                disk_max_bytes: Number(data.cache?.response?.disk_max_bytes || 16000000000),
                max_body_bytes: Number(data.cache?.response?.max_body_bytes || 67108864),
                semantic_key: data.cache?.response?.semantic_key ?? true,
                compression: data.cache?.response?.compression || 'gzip',
            },
        },
        auto_delete: {
            mode: normalizeAutoDeleteMode(data.auto_delete),
        },
        current_input_file: {
            enabled: currentInputFileEnabled,
            min_chars: Number(data.current_input_file?.min_chars ?? 0),
        },
        thinking_injection: {
            enabled: data.thinking_injection?.enabled ?? true,
            prompt: data.thinking_injection?.prompt || '',
            default_prompt: data.thinking_injection?.default_prompt || '',
        },
        prompt_limit: {
            enabled: data.prompt_limit?.enabled ?? true,
            max_chars_default: Number(data.prompt_limit?.max_chars_default || 380000),
            max_chars_expert: Number(data.prompt_limit?.max_chars_expert || 150000),
            max_chars_default_configured: Boolean(data.prompt_limit?.max_chars_default_configured),
            max_chars_expert_configured: Boolean(data.prompt_limit?.max_chars_expert_configured),
            auto_compress_enabled: data.prompt_limit?.auto_compress_enabled ?? false,
            compress_keep_recent: Number(data.prompt_limit?.compress_keep_recent || 6),
            compress_keep_system: data.prompt_limit?.compress_keep_system ?? true,
            pro_flash_compression_enabled: data.prompt_limit?.pro_flash_compression_enabled ?? false,
            pro_flash_compression_target_chars: Number(data.prompt_limit?.pro_flash_compression_target_chars || 150000),
            incremental_max_turns: Number(data.prompt_limit?.incremental_max_turns ?? 0),
            incremental_rotation_keep_recent: Number(data.prompt_limit?.incremental_rotation_keep_recent || 6),
        },
        safety: {
            enabled: Boolean(data.safety?.enabled),
            block_message: data.safety?.block_message || '',
            blocked_ips_text: listToText(data.safety?.blocked_ips),
            allowed_ips_text: listToText(data.safety?.allowed_ips),
            blocked_conversation_ids_text: listToText(data.safety?.blocked_conversation_ids),
            banned_content_text: listToText(data.safety?.banned_content),
            banned_regex_text: listToText(data.safety?.banned_regex),
            jailbreak: {
                enabled: Boolean(data.safety?.jailbreak?.enabled),
                patterns_text: listToText(data.safety?.jailbreak?.patterns),
            },
            auto_ban: {
                enabled: data.safety?.auto_ban?.enabled ?? true,
                threshold: safeNumber(data.safety?.auto_ban?.threshold, 3, 1, 1_000_000),
                window_seconds: safeNumber(data.safety?.auto_ban?.window_seconds, 600, 1, 30 * 24 * 60 * 60),
            },
            llm_check: {
                enabled: Boolean(data.safety?.llm_check?.enabled),
                model: data.safety?.llm_check?.model || 'deepseek-v4-flash-nothinking',
                timeout_ms: safeNumber(data.safety?.llm_check?.timeout_ms, 5000, 100, 60_000),
                fail_open: data.safety?.llm_check?.fail_open ?? true,
                cache_ttl_seconds: safeNumber(data.safety?.llm_check?.cache_ttl_seconds, 600, 1, 7 * 24 * 60 * 60),
                cache_max_entries: safeNumber(data.safety?.llm_check?.cache_max_entries, 10000, 1, 1_000_000),
                min_input_chars: safeNumber(data.safety?.llm_check?.min_input_chars, 30, 0, 1_000_000),
                max_input_chars: safeNumber(data.safety?.llm_check?.max_input_chars, 8000, 1, 1_000_000),
                max_concurrent: safeNumber(data.safety?.llm_check?.max_concurrent, 16, 1, 1024),
            },
        },
        model_aliases_text: JSON.stringify(data.model_aliases || {}, null, 2),
    }
}

function toServerPayload(form) {
    const currentInputFileEnabled = Boolean(form.current_input_file?.enabled)
    return {
        admin: { jwt_expire_hours: Number(form.admin.jwt_expire_hours) },
        runtime: {
            account_max_inflight: Number(form.runtime.account_max_inflight),
            account_max_queue: Number(form.runtime.account_max_queue),
            global_max_inflight: Number(form.runtime.global_max_inflight),
            token_refresh_interval_hours: Number(form.runtime.token_refresh_interval_hours),
            account_health_check_interval_minutes: Number(form.runtime.account_health_check_interval_minutes),
        },
        compat: {
            wide_input_strict_output: Boolean(form.compat?.wide_input_strict_output ?? true),
            strip_reference_markers: Boolean(form.compat?.strip_reference_markers ?? true),
        },
        responses: { store_ttl_seconds: Number(form.responses.store_ttl_seconds) },
        embeddings: { provider: String(form.embeddings.provider || '').trim() },
        cache: {
            response: {
                memory_ttl_seconds: Number(form.cache?.response?.memory_ttl_seconds || 300),
                memory_max_bytes: Number(form.cache?.response?.memory_max_bytes || 3800000000),
                disk_ttl_seconds: Number(form.cache?.response?.disk_ttl_seconds || 14400),
                disk_max_bytes: Number(form.cache?.response?.disk_max_bytes || 16000000000),
                max_body_bytes: Number(form.cache?.response?.max_body_bytes || 67108864),
                semantic_key: Boolean(form.cache?.response?.semantic_key ?? true),
            },
        },
        auto_delete: { mode: normalizeAutoDeleteMode(form.auto_delete) },
        current_input_file: {
            enabled: currentInputFileEnabled,
            min_chars: Number(form.current_input_file?.min_chars ?? 0),
        },
        thinking_injection: {
            enabled: Boolean(form.thinking_injection?.enabled ?? true),
            prompt: String(form.thinking_injection?.prompt || '').trim(),
        },
        prompt_limit: {
            enabled: Boolean(form.prompt_limit?.enabled ?? true),
            ...(form.prompt_limit?.max_chars_default_configured
                ? { max_chars_default: safeNumber(form.prompt_limit?.max_chars_default, 380000, 1, 10000000) }
                : {}),
            ...(form.prompt_limit?.max_chars_expert_configured
                ? { max_chars_expert: safeNumber(form.prompt_limit?.max_chars_expert, 150000, 1, 1000000) }
                : {}),
            auto_compress_enabled: Boolean(form.prompt_limit?.auto_compress_enabled ?? false),
            compress_keep_recent: safeNumber(form.prompt_limit?.compress_keep_recent, 6, 1, 100),
            compress_keep_system: Boolean(form.prompt_limit?.compress_keep_system ?? true),
            pro_flash_compression_enabled: Boolean(form.prompt_limit?.pro_flash_compression_enabled ?? false),
            pro_flash_compression_target_chars: safeNumber(form.prompt_limit?.pro_flash_compression_target_chars, 150000, 1, 1000000),
            incremental_max_turns: safeNumber(form.prompt_limit?.incremental_max_turns, 0, 0, 1000000),
            incremental_rotation_keep_recent: safeNumber(form.prompt_limit?.incremental_rotation_keep_recent, 6, 1, 100),
        },
        safety: {
            enabled: Boolean(form.safety?.enabled),
            block_message: String(form.safety?.block_message || '').trim(),
            blocked_ips: textToList(form.safety?.blocked_ips_text),
            allowed_ips: textToList(form.safety?.allowed_ips_text),
            blocked_conversation_ids: textToList(form.safety?.blocked_conversation_ids_text),
            banned_content: textToList(form.safety?.banned_content_text),
            banned_regex: textToList(form.safety?.banned_regex_text),
            jailbreak: {
                enabled: Boolean(form.safety?.jailbreak?.enabled),
                patterns: textToList(form.safety?.jailbreak?.patterns_text),
            },
            auto_ban: {
                enabled: Boolean(form.safety?.auto_ban?.enabled ?? true),
                threshold: safeNumber(form.safety?.auto_ban?.threshold, 3, 1, 1_000_000),
                window_seconds: safeNumber(form.safety?.auto_ban?.window_seconds, 600, 1, 30 * 24 * 60 * 60),
            },
            llm_check: {
                enabled: Boolean(form.safety?.llm_check?.enabled),
                model: String(form.safety?.llm_check?.model || 'deepseek-v4-flash-nothinking').trim(),
                timeout_ms: safeNumber(form.safety?.llm_check?.timeout_ms, 5000, 100, 60_000),
                fail_open: Boolean(form.safety?.llm_check?.fail_open ?? true),
                cache_ttl_seconds: safeNumber(form.safety?.llm_check?.cache_ttl_seconds, 600, 1, 7 * 24 * 60 * 60),
                cache_max_entries: safeNumber(form.safety?.llm_check?.cache_max_entries, 10000, 1, 1_000_000),
                min_input_chars: safeNumber(form.safety?.llm_check?.min_input_chars, 30, 0, 1_000_000),
                max_input_chars: safeNumber(form.safety?.llm_check?.max_input_chars, 8000, 1, 1_000_000),
                max_concurrent: safeNumber(form.safety?.llm_check?.max_concurrent, 16, 1, 1024),
            },
        },
    }
}

export function useSettingsForm({ apiFetch, t, onMessage, onRefresh, onForceLogout }) {
    const [loading, setLoading] = useState(false)
    const [saving, setSaving] = useState(false)
    const [changingPassword, setChangingPassword] = useState(false)
    const [importing, setImporting] = useState(false)
    const [exportData, setExportData] = useState(null)
    const [importMode, setImportMode] = useState('merge')
    const [importText, setImportText] = useState('')
    const [newPassword, setNewPassword] = useState('')
    const [settingsMeta, setSettingsMeta] = useState({
        default_password_warning: false,
        env_backed: false,
    })
    const [form, setForm] = useState(DEFAULT_FORM)

    const loadSettings = useCallback(async () => {
        setLoading(true)
        try {
            const { res, data } = await fetchSettings(apiFetch, t)
            if (!res.ok) {
                const detail = data.detail || t('settings.loadFailed')
                onMessage('error', detail)
                return
            }
            setSettingsMeta({
                default_password_warning: Boolean(data.admin?.default_password_warning),
                env_backed: Boolean(data.env_backed),
            })
            setForm(fromServerForm(data))
        } catch (e) {
            const detail = e?.message || t('settings.loadFailed')
            onMessage('error', detail)
            // eslint-disable-next-line no-console
            console.error(e)
        } finally {
            setLoading(false)
        }
    }, [apiFetch, onMessage, t])

    useEffect(() => {
        loadSettings()
    }, [loadSettings])

    const saveSettings = useCallback(async () => {
        let modelAliases = {}
        try {
            modelAliases = parseJSONMap(form.model_aliases_text, 'model_aliases', t)
        } catch (e) {
            onMessage('error', e.message)
            return
        }

        const payload = {
            ...toServerPayload(form),
            model_aliases: modelAliases,
        }

        setSaving(true)
        try {
            const { res, data } = await putSettings(apiFetch, payload)
            if (!res.ok) {
                onMessage('error', data.detail || t('settings.saveFailed'))
                return
            }
            onMessage('success', t('settings.saveSuccess'))
            if (typeof onRefresh === 'function') {
                onRefresh()
            }
            await loadSettings()
        } catch (e) {
            onMessage('error', t('settings.saveFailed'))
            // eslint-disable-next-line no-console
            console.error(e)
        } finally {
            setSaving(false)
        }
    }, [apiFetch, form, loadSettings, onMessage, onRefresh, t])

    const updatePassword = useCallback(async () => {
        if (String(newPassword || '').trim().length < 4) {
            onMessage('error', t('settings.passwordTooShort'))
            return
        }
        setChangingPassword(true)
        try {
            const { res, data } = await postPassword(apiFetch, newPassword.trim())
            if (!res.ok) {
                onMessage('error', data.detail || t('settings.passwordUpdateFailed'))
                return
            }
            onMessage('success', t('settings.passwordUpdated'))
            setNewPassword('')
            if (typeof onForceLogout === 'function') {
                onForceLogout()
            }
        } catch (_e) {
            onMessage('error', t('settings.passwordUpdateFailed'))
        } finally {
            setChangingPassword(false)
        }
    }, [apiFetch, newPassword, onForceLogout, onMessage, t])

    const loadExportData = useCallback(async () => {
        try {
            const { res, data } = await getExportData(apiFetch)
            if (!res.ok) {
                onMessage('error', data.detail || t('settings.exportFailed'))
                return null
            }
            setExportData(data)
            onMessage('success', t('settings.exportLoaded'))
            return data
        } catch (_e) {
            onMessage('error', t('settings.exportFailed'))
            return null
        }
    }, [apiFetch, onMessage, t])

    const downloadExportFile = useCallback(async () => {
        let latest = exportData
        if (!latest?.json) {
            const loaded = await loadExportData()
            if (!loaded) {
                return
            }
            latest = loaded
        }
        const jsonText = String(latest?.json || '').trim()
        if (!jsonText) {
            onMessage('error', t('settings.exportFailed'))
            return
        }
        const blob = new Blob([jsonText], { type: 'application/json;charset=utf-8' })
        const url = URL.createObjectURL(blob)
        const now = new Date()
        const pad = (n) => String(n).padStart(2, '0')
        const filename = `deepseek-web-to-api-config-backup-${now.getFullYear()}${pad(now.getMonth() + 1)}${pad(now.getDate())}-${pad(now.getHours())}${pad(now.getMinutes())}${pad(now.getSeconds())}.json`
        const link = document.createElement('a')
        link.href = url
        link.download = filename
        document.body.appendChild(link)
        link.click()
        document.body.removeChild(link)
        URL.revokeObjectURL(url)
        onMessage('success', t('settings.exportDownloaded'))
    }, [exportData, loadExportData, onMessage, t])

    const loadImportFile = useCallback((file) => {
        if (!file) return
        const reader = new FileReader()
        reader.onload = () => {
            const text = String(reader.result || '')
            setImportText(text)
            onMessage('success', t('settings.importFileLoaded'))
        }
        reader.onerror = () => {
            onMessage('error', t('settings.importFileReadFailed'))
        }
        reader.readAsText(file, 'utf-8')
    }, [onMessage, t])

    const doImport = useCallback(async () => {
        if (!String(importText || '').trim()) {
            onMessage('error', t('settings.importEmpty'))
            return
        }
        let parsed
        try {
            parsed = JSON.parse(importText)
        } catch (_e) {
            onMessage('error', t('settings.importInvalidJson'))
            return
        }
        setImporting(true)
        try {
            const { res, data } = await postImportData(apiFetch, importMode, parsed)
            if (!res.ok) {
                onMessage('error', data.detail || t('settings.importFailed'))
                return
            }
            onMessage('success', t('settings.importSuccess', { mode: importMode }))
            if (typeof onRefresh === 'function') {
                onRefresh()
            }
            await loadSettings()
        } catch (_e) {
            onMessage('error', t('settings.importFailed'))
        } finally {
            setImporting(false)
        }
    }, [apiFetch, importMode, importText, loadSettings, onMessage, onRefresh, t])

    return {
        form,
        setForm,
        loading,
        saving,
        changingPassword,
        importing,
        exportData,
        importMode,
        setImportMode,
        importText,
        setImportText,
        newPassword,
        setNewPassword,
        settingsMeta,
        saveSettings,
        updatePassword,
        loadExportData,
        downloadExportFile,
        loadImportFile,
        doImport,
    }
}
