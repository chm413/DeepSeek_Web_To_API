import { useEffect, useRef, useState } from 'react'
import { FileJson, Loader2, LockKeyhole, Upload, X } from 'lucide-react'

const MAX_BATCH_UPLOAD_BYTES = 16 * 1024 * 1024
const MAX_BATCH_UPLOAD_ACCOUNTS = 5000
const USE_FILE_DEFAULT = '__use_file_default__'

function isRecord(value) {
    return Boolean(value) && typeof value === 'object' && !Array.isArray(value)
}

function uploadDefaultsFromPayload(payload) {
    const defaults = isRecord(payload?.defaults) ? payload.defaults : {}
    const hasDefault = key => Object.prototype.hasOwnProperty.call(defaults, key)

    return {
        enabled: typeof defaults.enabled === 'boolean'
            ? (defaults.enabled ? 'enabled' : 'disabled')
            : USE_FILE_DEFAULT,
        proxyID: hasDefault('proxy_id') ? String(defaults.proxy_id ?? '') : USE_FILE_DEFAULT,
        autoRoute: typeof defaults.proxy_auto_route === 'boolean'
            ? (defaults.proxy_auto_route ? 'enabled' : 'disabled')
            : USE_FILE_DEFAULT,
    }
}

function applyUploadDefaults(payload, duplicatePolicy, uploadDefaults, dryRun) {
    const request = {
        ...payload,
        on_duplicate: duplicatePolicy,
        dry_run: dryRun,
    }
    const defaults = isRecord(payload.defaults) ? { ...payload.defaults } : {}

    if (uploadDefaults.enabled !== USE_FILE_DEFAULT) {
        defaults.enabled = uploadDefaults.enabled === 'enabled'
    }
    if (uploadDefaults.proxyID !== USE_FILE_DEFAULT) {
        defaults.proxy_id = uploadDefaults.proxyID
    }
    if (uploadDefaults.autoRoute !== USE_FILE_DEFAULT) {
        defaults.proxy_auto_route = uploadDefaults.autoRoute === 'enabled'
    }

    if (Object.keys(defaults).length > 0) {
        request.defaults = defaults
    } else {
        delete request.defaults
    }
    return request
}

function validateUploadLimits(payload, t) {
    if (!payload || !Array.isArray(payload.accounts) || payload.accounts.length === 0) {
        return t('accountManager.batchUploadInvalidFile')
    }
    if (payload.accounts.length > MAX_BATCH_UPLOAD_ACCOUNTS) {
        return t('accountManager.batchUploadTooManyAccounts', { count: MAX_BATCH_UPLOAD_ACCOUNTS })
    }
    if (new Blob([JSON.stringify(payload)]).size > MAX_BATCH_UPLOAD_BYTES) {
        return t('accountManager.batchUploadRequestTooLarge')
    }
    return ''
}

function proxyOptionLabel(proxy, t) {
    const name = proxy.name || `${proxy.host}:${proxy.port}`
    const state = proxy.route_available
        ? t('accountManager.proxyAvailable')
        : proxy.last_test_at_unix
            ? t('accountManager.proxyUnavailable')
            : t('accountManager.proxyUntested')
    const latency = Number(proxy.last_latency_ms) > 0 ? `${proxy.last_latency_ms}ms` : '-'
    const region = [proxy.last_country, proxy.last_colo].filter(Boolean).join('/') || '-'
    return `${name} | ${state} | ${latency} | ${region}`
}

export default function BatchAccountUploadModal({
    show,
    t,
    loading,
    onClose,
    onUpload,
    proxies = [],
    autoRouteEnabled = false,
}) {
    const inputRef = useRef(null)
    const [payload, setPayload] = useState(null)
    const [fileName, setFileName] = useState('')
    const [duplicatePolicy, setDuplicatePolicy] = useState('skip')
    const [uploadDefaults, setUploadDefaults] = useState({
        enabled: USE_FILE_DEFAULT,
        proxyID: USE_FILE_DEFAULT,
        autoRoute: USE_FILE_DEFAULT,
    })
    const [result, setResult] = useState(null)
    const [error, setError] = useState('')

    const reset = () => {
        setPayload(null)
        setFileName('')
        setDuplicatePolicy('skip')
        setUploadDefaults({
            enabled: USE_FILE_DEFAULT,
            proxyID: USE_FILE_DEFAULT,
            autoRoute: USE_FILE_DEFAULT,
        })
        setResult(null)
        setError('')
        if (inputRef.current) inputRef.current.value = ''
    }

    useEffect(() => {
        if (!show) reset()
    }, [show])

    if (!show) return null

    const close = () => {
        reset()
        onClose()
    }
    const selectedDefaultProxyIsKnown = uploadDefaults.proxyID === USE_FILE_DEFAULT ||
        uploadDefaults.proxyID === '' ||
        proxies.some(proxy => proxy.id === uploadDefaults.proxyID)

    const selectFile = async (event) => {
        const file = event.target.files?.[0]
        reset()
        if (!file) return
        try {
            if (file.size > MAX_BATCH_UPLOAD_BYTES) {
                throw new Error(t('accountManager.batchUploadFileTooLarge'))
            }
            const parsed = JSON.parse(await file.text())
            const nextPayload = Array.isArray(parsed) ? { accounts: parsed } : parsed
            const limitError = validateUploadLimits(nextPayload, t)
            if (limitError) {
                throw new Error(limitError)
            }
            setPayload(nextPayload)
            setFileName(file.name)
            setDuplicatePolicy(nextPayload.on_duplicate === 'update' ? 'update' : 'skip')
            setUploadDefaults(uploadDefaultsFromPayload(nextPayload))
        } catch (err) {
            setError(err.message || t('accountManager.batchUploadInvalidFile'))
        }
    }

    const submit = async (dryRun) => {
        if (!payload) return
        setError('')
        try {
            const requestPayload = applyUploadDefaults(payload, duplicatePolicy, uploadDefaults, dryRun)
            const limitError = validateUploadLimits(requestPayload, t)
            if (limitError) {
                throw new Error(limitError)
            }
            const data = await onUpload(requestPayload)
            setResult(data)
            if (!dryRun && Number(data.invalid || 0) === 0) {
                close()
            }
        } catch (err) {
            setError(err.message || t('messages.requestFailed'))
        }
    }

    return (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/45 backdrop-blur-sm p-4 animate-in fade-in">
            <div className="ops-panel w-full max-w-xl overflow-hidden animate-in zoom-in-95">
                <div className="flex items-center justify-between border-b border-border p-4">
                    <div className="flex items-center gap-2">
                        <Upload className="h-4 w-4 text-primary" />
                        <h3 className="font-black">{t('accountManager.batchUploadTitle')}</h3>
                    </div>
                    <button onClick={close} className="text-muted-foreground hover:text-foreground" title={t('actions.cancel')}>
                        <X className="h-5 w-5" />
                    </button>
                </div>
                <div className="space-y-4 p-5">
                    <input ref={inputRef} type="file" accept="application/json,.json" onChange={selectFile} className="hidden" />
                    <button type="button" onClick={() => inputRef.current?.click()} className="flex min-h-28 w-full items-center justify-center gap-3 rounded-md border border-dashed border-border bg-muted/20 px-4 text-sm font-bold hover:border-primary/50 hover:bg-blue-50/40">
                        <FileJson className="h-6 w-6 text-primary" />
                        <span>{fileName || t('accountManager.batchUploadSelectFile')}</span>
                    </button>
                    <p className="text-xs text-muted-foreground">{t('accountManager.batchUploadLimits')}</p>

                    {payload && (
                        <div className="space-y-3 rounded-md border border-border bg-muted/20 p-3 text-sm">
                            <div className="grid grid-cols-2 gap-3">
                                <div>
                                    <div className="text-xs font-bold text-muted-foreground">{t('accountManager.batchUploadAccounts')}</div>
                                    <div className="mt-1 text-lg font-black tabular-nums">{payload.accounts.length}</div>
                                </div>
                                <div>
                                    <label className="text-xs font-bold text-muted-foreground" htmlFor="batch-duplicate-policy">{t('accountManager.batchUploadDuplicatePolicy')}</label>
                                    <select id="batch-duplicate-policy" value={duplicatePolicy} onChange={event => setDuplicatePolicy(event.target.value)} className="input-field mt-1 h-8 min-h-8 py-1 text-xs">
                                        <option value="skip">{t('accountManager.batchUploadSkip')}</option>
                                        <option value="update">{t('accountManager.batchUploadUpdate')}</option>
                                    </select>
                                </div>
                            </div>
                            <div className="border-t border-border pt-3">
                                <div className="text-xs font-bold text-muted-foreground">{t('accountManager.batchUploadDefaults')}</div>
                                <p className="mt-1 text-xs text-muted-foreground">{t('accountManager.batchUploadDefaultsHint')}</p>
                                <div className="mt-3 grid gap-3 sm:grid-cols-3">
                                    <div>
                                        <label className="text-xs font-bold text-muted-foreground" htmlFor="batch-default-enabled">{t('accountManager.batchUploadDefaultEnabled')}</label>
                                        <select
                                            id="batch-default-enabled"
                                            value={uploadDefaults.enabled}
                                            onChange={event => setUploadDefaults(current => ({ ...current, enabled: event.target.value }))}
                                            className="input-field mt-1 h-8 min-h-8 w-full py-1 text-xs"
                                        >
                                            <option value={USE_FILE_DEFAULT}>{t('accountManager.batchUploadUseFileValue')}</option>
                                            <option value="enabled">{t('accountManager.batchUploadEnabled')}</option>
                                            <option value="disabled">{t('accountManager.batchUploadDisabled')}</option>
                                        </select>
                                    </div>
                                    <div>
                                        <label className="text-xs font-bold text-muted-foreground" htmlFor="batch-default-proxy">{t('accountManager.batchUploadDefaultProxy')}</label>
                                        <select
                                            id="batch-default-proxy"
                                            value={uploadDefaults.proxyID}
                                            onChange={event => setUploadDefaults(current => ({ ...current, proxyID: event.target.value }))}
                                            className="input-field mt-1 h-8 min-h-8 w-full py-1 text-xs"
                                        >
                                            <option value={USE_FILE_DEFAULT}>{t('accountManager.batchUploadUseFileValue')}</option>
                                            <option value="">{t('accountManager.proxyNone')}</option>
                                            {!selectedDefaultProxyIsKnown && (
                                                <option value={uploadDefaults.proxyID}>{t('accountManager.batchUploadFileProxyValue', { proxy: uploadDefaults.proxyID })}</option>
                                            )}
                                            {proxies.map(proxy => (
                                                <option key={proxy.id} value={proxy.id}>{proxyOptionLabel(proxy, t)}</option>
                                            ))}
                                        </select>
                                    </div>
                                    <div>
                                        <label className="text-xs font-bold text-muted-foreground" htmlFor="batch-default-auto-route">{t('accountManager.batchUploadDefaultAutoRoute')}</label>
                                        <select
                                            id="batch-default-auto-route"
                                            value={uploadDefaults.autoRoute}
                                            onChange={event => setUploadDefaults(current => ({ ...current, autoRoute: event.target.value }))}
                                            className="input-field mt-1 h-8 min-h-8 w-full py-1 text-xs"
                                        >
                                            <option value={USE_FILE_DEFAULT}>{t('accountManager.batchUploadUseFileValue')}</option>
                                            <option value="enabled" disabled={!autoRouteEnabled}>{t('accountManager.batchUploadAutoRouteEnabled')}</option>
                                            <option value="disabled">{t('accountManager.batchUploadAutoRouteDisabled')}</option>
                                        </select>
                                        <p className="mt-1 text-[10px] text-muted-foreground">{t('accountManager.batchUploadAutoRouteHint')}</p>
                                    </div>
                                </div>
                            </div>
                        </div>
                    )}

                    <div className="flex items-center gap-2 text-xs text-muted-foreground" title={t('accountManager.batchUploadCredentialProtection')}>
                        <LockKeyhole className="h-3.5 w-3.5 text-emerald-600" />
                        <span>{t('accountManager.batchUploadCredentialProtection')}</span>
                    </div>

                    {error && <div className="rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700" role="alert">{error}</div>}
                    {result && (
                        <div className="grid grid-cols-4 gap-2 rounded-md border border-border p-3 text-center text-xs">
                            <div><div className="font-black text-emerald-700">{result.created || 0}</div><div className="text-muted-foreground">{t('accountManager.batchCreated')}</div></div>
                            <div><div className="font-black text-blue-700">{result.updated || 0}</div><div className="text-muted-foreground">{t('accountManager.batchUpdated')}</div></div>
                            <div><div className="font-black text-slate-700">{result.skipped || 0}</div><div className="text-muted-foreground">{t('accountManager.batchSkipped')}</div></div>
                            <div><div className="font-black text-red-700">{result.invalid || 0}</div><div className="text-muted-foreground">{t('accountManager.batchInvalid')}</div></div>
                        </div>
                    )}

                    <div className="flex justify-end gap-2 pt-1">
                        <button onClick={close} className="btn btn-secondary">{t('actions.cancel')}</button>
                        <button onClick={() => submit(true)} disabled={!payload || loading} className="btn btn-secondary">
                            {loading ? <Loader2 className="h-4 w-4 animate-spin" /> : null}
                            {t('accountManager.batchUploadValidate')}
                        </button>
                        <button onClick={() => submit(false)} disabled={!payload || loading} className="btn btn-primary">
                            {loading ? <Loader2 className="h-4 w-4 animate-spin" /> : <Upload className="h-4 w-4" />}
                            {t('accountManager.batchUploadImport')}
                        </button>
                    </div>
                </div>
            </div>
        </div>
    )
}
