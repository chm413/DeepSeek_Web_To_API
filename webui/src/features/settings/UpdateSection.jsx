import { useCallback, useEffect, useMemo, useState } from 'react'
import {
    CheckCircle2,
    CircleAlert,
    CloudDownload,
    Download,
    Loader2,
    RefreshCw,
    RotateCcw,
    Save,
} from 'lucide-react'

const EMPTY_SETTINGS = {
    enabled: true,
    auto_download: false,
    auto_apply: false,
    check_interval_minutes: 360,
}

function normalizeSettings(settings) {
    return {
        enabled: settings?.enabled ?? EMPTY_SETTINGS.enabled,
        auto_download: Boolean(settings?.auto_download),
        auto_apply: Boolean(settings?.auto_apply),
        check_interval_minutes: Number(settings?.check_interval_minutes) || EMPTY_SETTINGS.check_interval_minutes,
    }
}

function detailFromResponse(data, fallback) {
    return String(data?.detail || data?.error || data?.message || fallback)
}

async function readJSON(res) {
    const contentType = String(res.headers.get('content-type') || '').toLowerCase()
    if (!contentType.includes('application/json')) {
        return {}
    }
    return res.json()
}

function formatCheckedAt(value, t) {
    const seconds = Number(value)
    if (!Number.isFinite(seconds) || seconds <= 0) {
        return t('settings.appUpdateNeverChecked')
    }
    return new Date(seconds * 1000).toLocaleString()
}

function StatusRow({ label, value, mono = false }) {
    return (
        <div className="flex min-w-0 items-center justify-between gap-4 border-b border-border/60 py-2 last:border-b-0">
            <span className="shrink-0 text-xs text-muted-foreground">{label}</span>
            <span className={`min-w-0 truncate text-right text-xs font-medium ${mono ? 'font-mono' : ''}`}>{value || '-'}</span>
        </div>
    )
}

export default function UpdateSection({ t, authFetch, onMessage, onStatusChange }) {
    const [update, setUpdate] = useState(null)
    const [settings, setSettings] = useState(EMPTY_SETTINGS)
    const [settingsDirty, setSettingsDirty] = useState(false)
    const [loading, setLoading] = useState(true)
    const [busyAction, setBusyAction] = useState('')
    const [endpointUnavailable, setEndpointUnavailable] = useState(false)

    const applyResponse = useCallback((data, { preserveDirty = false } = {}) => {
        if (!data || typeof data !== 'object') return
        setUpdate(data)
        if (!preserveDirty && data.settings) {
            setSettings(normalizeSettings(data.settings))
            setSettingsDirty(false)
        }
        if (typeof onStatusChange === 'function') {
            onStatusChange(data)
        }
    }, [onStatusChange])

    const loadStatus = useCallback(async ({ silent = false } = {}) => {
        if (!silent) setLoading(true)
        try {
            const res = await authFetch('/admin/updates')
            const data = await readJSON(res)
            if (res.status === 404) {
                setEndpointUnavailable(true)
                applyResponse({ settings: EMPTY_SETTINGS, status: { supported: false } }, { preserveDirty: settingsDirty })
                return null
            }
            if (!res.ok) {
                throw new Error(detailFromResponse(data, t('settings.appUpdateLoadFailed')))
            }
            setEndpointUnavailable(false)
            applyResponse(data, { preserveDirty: settingsDirty })
            return data
        } catch (error) {
            if (!silent) {
                onMessage?.('error', error?.message || t('settings.appUpdateLoadFailed'))
            }
            return null
        } finally {
            if (!silent) setLoading(false)
        }
    }, [applyResponse, authFetch, onMessage, settingsDirty, t])

    useEffect(() => {
        loadStatus()
        const timer = window.setInterval(() => loadStatus({ silent: true }), 30_000)
        return () => window.clearInterval(timer)
    }, [loadStatus])

    const runAction = useCallback(async (action, endpoint, successKey) => {
        setBusyAction(action)
        try {
            const res = await authFetch(endpoint, { method: 'POST' })
            const data = await readJSON(res)
            if (!res.ok) {
                throw new Error(detailFromResponse(data, t('settings.appUpdateActionFailed')))
            }
            applyResponse(data, { preserveDirty: true })
            await loadStatus({ silent: true })
            onMessage?.('success', t(successKey))
            return true
        } catch (error) {
            onMessage?.('error', error?.message || t('settings.appUpdateActionFailed'))
            return false
        } finally {
            setBusyAction('')
        }
    }, [applyResponse, authFetch, loadStatus, onMessage, t])

    const saveUpdateSettings = useCallback(async () => {
        setBusyAction('settings')
        try {
            const payload = {
                enabled: Boolean(settings.enabled),
                auto_download: Boolean(settings.auto_download),
                auto_apply: Boolean(settings.auto_apply),
                check_interval_minutes: Math.max(5, Math.min(10080, Number(settings.check_interval_minutes) || EMPTY_SETTINGS.check_interval_minutes)),
            }
            const res = await authFetch('/admin/updates/settings', {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(payload),
            })
            const data = await readJSON(res)
            if (!res.ok) {
                throw new Error(detailFromResponse(data, t('settings.appUpdateSaveFailed')))
            }
            applyResponse(data)
            setSettingsDirty(false)
            await loadStatus({ silent: true })
            onMessage?.('success', t('settings.appUpdateSaveSuccess'))
        } catch (error) {
            onMessage?.('error', error?.message || t('settings.appUpdateSaveFailed'))
        } finally {
            setBusyAction('')
        }
    }, [applyResponse, authFetch, loadStatus, onMessage, settings, t])

    const status = update?.status || {}
    const supported = Boolean(status.supported)
    const operationActive = Boolean(status.checking || status.downloading || status.applying)
    const latestTag = status.latest_tag || ''
    const currentTag = status.current_tag || update?.current_tag || update?.current_version || ''
    const downloadedTag = status.downloaded_tag || ''
    const installedTag = status.installed_tag || ''
    const failedTag = status.failed_tag || ''
    const downloadable = Boolean(status.downloadable)
    const isBusy = Boolean(busyAction) || operationActive
    const phase = useMemo(() => {
        if (status.applying || busyAction === 'apply') return { label: t('settings.appUpdateApplying'), tone: 'text-amber-700', icon: Loader2 }
        if (status.downloading || busyAction === 'download') return { label: t('settings.appUpdateDownloading'), tone: 'text-blue-700', icon: Loader2 }
        if (status.checking || busyAction === 'check') return { label: t('settings.appUpdateChecking'), tone: 'text-blue-700', icon: Loader2 }
        if (status.last_error) return { label: t('settings.appUpdateError'), tone: 'text-rose-700', icon: CircleAlert }
        return { label: t('settings.appUpdateReady'), tone: 'text-emerald-700', icon: CheckCircle2 }
    }, [busyAction, status.applying, status.checking, status.downloading, status.last_error, t])
    const PhaseIcon = phase.icon

    const updateSettings = (patch) => {
        setSettings((previous) => ({ ...previous, ...patch }))
        setSettingsDirty(true)
    }

    const applyUpdate = async () => {
        if (!confirm(t('settings.appUpdateApplyConfirm', { version: downloadedTag || latestTag }))) return
        await runAction('apply', '/admin/updates/apply', 'settings.appUpdateApplyStarted')
    }

    const rollback = async () => {
        if (!confirm(t('settings.appUpdateRollbackConfirm'))) return
        await runAction('rollback', '/admin/updates/rollback', 'settings.appUpdateRollbackStarted')
    }

    return (
        <section className="bg-card border border-border rounded-xl p-5 space-y-4" aria-busy={loading || isBusy}>
            <div className="flex flex-wrap items-start justify-between gap-3">
                <div className="flex min-w-0 items-center gap-2">
                    <CloudDownload className="w-4 h-4 shrink-0 text-muted-foreground" />
                    <div className="min-w-0">
                        <h3 className="font-semibold">{t('settings.appUpdateTitle')}</h3>
                        <p className="mt-1 text-sm text-muted-foreground">{t('settings.appUpdateDesc')}</p>
                    </div>
                </div>
                <span className={`inline-flex shrink-0 items-center gap-1.5 rounded-md border border-current/20 bg-background px-2 py-1 text-[11px] font-semibold ${phase.tone}`}>
                    <PhaseIcon className={`h-3.5 w-3.5 ${phase.icon === Loader2 ? 'animate-spin' : ''}`} />
                    {phase.label}
                </span>
            </div>

            {!loading && !supported && (
                <div className="rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-xs leading-5 text-amber-800">
                    {endpointUnavailable ? t('settings.appUpdateEndpointUnavailable') : t('settings.appUpdateUnsupported')}
                </div>
            )}

            <div className="grid grid-cols-1 gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(250px,0.8fr)]">
                <div className="space-y-3">
                    <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                        <label className="flex items-start gap-3 rounded-lg border border-border bg-background/60 p-3">
                            <input
                                type="checkbox"
                                checked={Boolean(settings.enabled)}
                                disabled={isBusy}
                                onChange={(event) => updateSettings({ enabled: event.target.checked })}
                                className="mt-0.5 h-4 w-4 rounded border-border disabled:opacity-50"
                            />
                            <span className="space-y-1">
                                <span className="block text-sm font-medium">{t('settings.appUpdateEnabled')}</span>
                                <span className="block text-xs text-muted-foreground">{t('settings.appUpdateEnabledDesc')}</span>
                            </span>
                        </label>
                        <label className="flex items-start gap-3 rounded-lg border border-border bg-background/60 p-3">
                            <input
                                type="checkbox"
                                checked={Boolean(settings.auto_download)}
                                disabled={isBusy || !settings.enabled || !supported}
                                onChange={(event) => updateSettings({ auto_download: event.target.checked })}
                                className="mt-0.5 h-4 w-4 rounded border-border disabled:opacity-50"
                            />
                            <span className="space-y-1">
                                <span className="block text-sm font-medium">{t('settings.appUpdateAutoDownload')}</span>
                                <span className="block text-xs text-muted-foreground">{t('settings.appUpdateAutoDownloadDesc')}</span>
                            </span>
                        </label>
                        <label className="flex items-start gap-3 rounded-lg border border-border bg-background/60 p-3">
                            <input
                                type="checkbox"
                                checked={Boolean(settings.auto_apply)}
                                disabled={isBusy || !settings.enabled || !settings.auto_download || !supported}
                                onChange={(event) => updateSettings({ auto_apply: event.target.checked })}
                                className="mt-0.5 h-4 w-4 rounded border-border disabled:opacity-50"
                            />
                            <span className="space-y-1">
                                <span className="block text-sm font-medium">{t('settings.appUpdateAutoApply')}</span>
                                <span className="block text-xs text-muted-foreground">{t('settings.appUpdateAutoApplyDesc')}</span>
                            </span>
                        </label>
                        <label className="text-sm space-y-2 rounded-lg border border-border bg-background/60 p-3">
                            <span className="block text-muted-foreground">{t('settings.appUpdateInterval')}</span>
                            <input
                                type="number"
                                min={5}
                                max={10080}
                                value={settings.check_interval_minutes}
                                disabled={isBusy || !settings.enabled}
                                onChange={(event) => updateSettings({ check_interval_minutes: Number(event.target.value || EMPTY_SETTINGS.check_interval_minutes) })}
                                className="w-full rounded-lg border border-border bg-background px-3 py-2 text-sm disabled:opacity-50"
                            />
                        </label>
                    </div>
                    <button
                        type="button"
                        onClick={saveUpdateSettings}
                        disabled={isBusy || !settingsDirty}
                        className="btn btn-secondary h-9 disabled:cursor-not-allowed disabled:opacity-50"
                    >
                        {busyAction === 'settings' ? <Loader2 className="h-4 w-4 animate-spin" /> : <Save className="h-4 w-4" />}
                        {t('settings.appUpdateSave')}
                    </button>
                </div>

                <div className="rounded-lg border border-border bg-background/50 px-3">
                    <StatusRow label={t('settings.appUpdateCurrent')} value={currentTag} mono />
                    <StatusRow label={t('settings.appUpdateLatest')} value={latestTag || t('settings.appUpdateNoRelease')} mono />
                    <StatusRow label={t('settings.appUpdateCheckedAt')} value={formatCheckedAt(status.checked_at_unix, t)} />
                    <StatusRow label={t('settings.appUpdateDownloaded')} value={downloadedTag || t('settings.appUpdateNone')} mono />
                    <StatusRow label={t('settings.appUpdateInstalled')} value={installedTag || t('settings.appUpdateNone')} mono />
                    <StatusRow label={t('settings.appUpdateFailedCandidate')} value={failedTag || t('settings.appUpdateNone')} mono />
                </div>
            </div>

            {failedTag && (
                <div className="rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-xs leading-5 text-amber-800">
                    {t('settings.appUpdateFailedCandidateDesc', { version: failedTag })}
                </div>
            )}

            {status.last_error && (
                <div className="rounded-lg border border-rose-200 bg-rose-50 px-3 py-2 text-xs leading-5 text-rose-800 break-words">
                    {status.last_error}
                </div>
            )}

            <div className="flex flex-wrap gap-2 border-t border-border pt-4">
                <button
                    type="button"
                    onClick={() => runAction('check', '/admin/updates/check', 'settings.appUpdateChecked')}
                    disabled={isBusy}
                    className="btn btn-secondary h-9 disabled:cursor-not-allowed disabled:opacity-50"
                >
                    {busyAction === 'check' ? <Loader2 className="h-4 w-4 animate-spin" /> : <RefreshCw className="h-4 w-4" />}
                    {t('settings.appUpdateCheckNow')}
                </button>
                <button
                    type="button"
                    onClick={() => runAction('download', '/admin/updates/download', 'settings.appUpdateDownloadedSuccess')}
                    disabled={isBusy || !supported || !status.update_available || !downloadable}
                    className="btn btn-secondary h-9 disabled:cursor-not-allowed disabled:opacity-50"
                >
                    {busyAction === 'download' ? <Loader2 className="h-4 w-4 animate-spin" /> : <Download className="h-4 w-4" />}
                    {t('settings.appUpdateDownload')}
                </button>
                <button
                    type="button"
                    onClick={applyUpdate}
                    disabled={isBusy || !supported || !downloadedTag}
                    className="btn btn-primary h-9 disabled:cursor-not-allowed disabled:opacity-50"
                >
                    {busyAction === 'apply' ? <Loader2 className="h-4 w-4 animate-spin" /> : <CloudDownload className="h-4 w-4" />}
                    {t('settings.appUpdateApply')}
                </button>
                <button
                    type="button"
                    onClick={rollback}
                    disabled={isBusy || !supported || !installedTag}
                    className="btn btn-secondary h-9 disabled:cursor-not-allowed disabled:opacity-50"
                >
                    {busyAction === 'rollback' ? <Loader2 className="h-4 w-4 animate-spin" /> : <RotateCcw className="h-4 w-4" />}
                    {t('settings.appUpdateRollback')}
                </button>
            </div>
        </section>
    )
}
