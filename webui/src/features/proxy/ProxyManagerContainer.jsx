import { useEffect, useState } from 'react'
import { createPortal } from 'react-dom'
import { Cpu, Pencil, Play, Plus, RefreshCw, Save, Shield, Trash2, X } from 'lucide-react'
import clsx from 'clsx'

import { useI18n } from '../../i18n'

async function readApiResponse(res, nonJsonMessage) {
    const contentType = String(res.headers.get('content-type') || '').toLowerCase()
    const raw = await res.text()
    const trimmed = raw.trim()

    if (!trimmed) {
        return {}
    }

    if (contentType.includes('application/json')) {
        try {
            return JSON.parse(trimmed)
        } catch (_err) {
            if (!res.ok) {
                return { detail: trimmed }
            }
            throw new Error(nonJsonMessage)
        }
    }

    if (!res.ok) {
        return { detail: trimmed }
    }

    throw new Error(nonJsonMessage)
}

const EMPTY_FORM = {
    name: '',
    type: 'socks5h',
    host: '',
    port: 1080,
    username: '',
    password: '',
    uri: '',
}

const CORE_PROXY_TYPES = new Set(['vless', 'vmess', 'hysteria2'])

function isCoreProxyType(type) {
    return CORE_PROXY_TYPES.has(String(type || '').toLowerCase())
}

function createEmptyProxyForm() {
    return { ...EMPTY_FORM }
}

function ProxyStatusBadge({ t, result, testing = false }) {
    if (testing) {
        return (
            <span className="inline-flex items-center rounded-full border border-border bg-muted/40 px-2 py-1 text-[10px] font-medium text-muted-foreground">
                {t('proxyManager.testing')}
            </span>
        )
    }
    if (!result) {
        return (
            <span className="inline-flex items-center rounded-full border border-border bg-muted/20 px-2 py-1 text-[10px] font-medium text-muted-foreground">
                {t('proxyManager.untested')}
            </span>
        )
    }
    return (
        <span
            className={clsx(
                'inline-flex items-center rounded-full border px-2 py-1 text-[10px] font-medium',
                result.success
                    ? 'border-emerald-500/20 bg-emerald-500/10 text-emerald-500'
                    : 'border-destructive/20 bg-destructive/10 text-destructive'
            )}
        >
            {result.success
                ? t('proxyManager.testSuccessShort', { time: result.response_time ?? 0 })
                : t('proxyManager.testFailedShort')}
        </span>
    )
}

function ProxiesTable({
    t,
    proxies,
    testing,
    testResults,
    onCreate,
    onTest,
    onEdit,
    onDelete,
}) {
    return (
        <div className="ops-panel overflow-hidden">
            <div className="p-4 border-b border-border flex flex-col md:flex-row md:items-center justify-between gap-4">
                <div>
                    <h2 className="text-lg font-semibold">{t('proxyManager.title')}</h2>
                    <p className="text-sm text-muted-foreground">{t('proxyManager.desc')}</p>
                </div>
                <button
                    onClick={onCreate}
                    className="btn btn-primary"
                >
                    <Plus className="w-4 h-4" />
                    {t('proxyManager.addProxy')}
                </button>
            </div>

            {proxies.length === 0 ? (
                <div className="p-10 text-center text-muted-foreground">{t('proxyManager.noProxies')}</div>
            ) : (
                <div className="divide-y divide-border">
                    {proxies.map((proxy) => {
                        const result = testResults[proxy.id]
                        return (
                            <div key={proxy.id} className="page-transition p-4 md:p-5 flex flex-col lg:flex-row lg:items-center justify-between gap-4 hover:bg-blue-50/45 transition-colors">
                                <div className="min-w-0">
                                    <div className="flex flex-wrap items-center gap-2">
                                        <div className="font-medium text-foreground">{proxy.name || `${proxy.host}:${proxy.port}`}</div>
                                        <span className="inline-flex items-center rounded-full border border-primary/20 bg-primary/10 px-2 py-1 text-[10px] font-medium uppercase tracking-wide text-primary">
                                            {proxy.type}
                                        </span>
                                        {proxy.username && (
                                            <span className="inline-flex items-center gap-1 rounded-full border border-border bg-muted/20 px-2 py-1 text-[10px] font-medium text-muted-foreground">
                                                <Shield className="w-3 h-3" />
                                                {proxy.username}
                                            </span>
                                        )}
                                        {proxy.core_managed && (
                                            <span className="inline-flex items-center gap-1 rounded-full border border-cyan-300/60 bg-cyan-50 px-2 py-1 text-[10px] font-medium text-cyan-700">
                                                <Cpu className="w-3 h-3" />
                                                Xray
                                            </span>
                                        )}
                                        <ProxyStatusBadge t={t} result={result} testing={testing[proxy.id]} />
                                    </div>
                                    <div className="mt-2 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                                        <span className="font-mono bg-muted/30 px-2 py-1 rounded border border-border">
                                            {proxy.host}:{proxy.port}
                                        </span>
                                        {proxy.has_password && (
                                            <span className="rounded-full border border-border bg-muted/20 px-2 py-1 text-[10px]">
                                                {t('proxyManager.authEnabled')}
                                            </span>
                                        )}
                                        {result?.message && (
                                            <span className="truncate max-w-full">{result.message}</span>
                                        )}
                                    </div>
                                </div>

                                <div className="flex items-center gap-2 self-start lg:self-auto">
                                    <button
                                        onClick={() => onTest(proxy)}
                                        disabled={testing[proxy.id]}
                                        className="btn btn-secondary btn-sm"
                                    >
                                        <Play className="w-3.5 h-3.5" />
                                        {t('proxyManager.testAction')}
                                    </button>
                                    <button
                                        onClick={() => onEdit(proxy)}
                                        className="p-2 text-muted-foreground hover:text-primary hover:bg-blue-50 rounded-md transition-colors"
                                        title={t('proxyManager.editProxy')}
                                    >
                                        <Pencil className="w-4 h-4" />
                                    </button>
                                    <button
                                        onClick={() => onDelete(proxy)}
                                        className="p-2 text-muted-foreground hover:text-destructive hover:bg-destructive/10 rounded-md transition-colors"
                                        title={t('proxyManager.deleteProxy')}
                                    >
                                        <Trash2 className="w-4 h-4" />
                                    </button>
                                </div>
                            </div>
                        )
                    })}
                </div>
            )}
        </div>
    )
}

function CoreStatusPanel({ t, status, form, setForm, loading, onRefresh, onSave }) {
    const available = Boolean(status?.available)
    return (
        <div className="ops-panel overflow-hidden" data-testid="xray-core-status">
            <div className="flex flex-col gap-3 border-b border-border px-4 py-3 md:flex-row md:items-center md:justify-between">
                <div className="flex min-w-0 items-center gap-3">
                    <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-md border border-cyan-200 bg-cyan-50 text-cyan-700">
                        <Cpu className="h-4 w-4" />
                    </div>
                    <div className="min-w-0">
                        <div className="flex flex-wrap items-center gap-2">
                            <h2 className="text-sm font-black">{t('proxyManager.coreTitle')}</h2>
                            <span className={clsx(
                                'rounded-full border px-2 py-0.5 text-[10px] font-bold',
                                available
                                    ? 'border-emerald-300/60 bg-emerald-50 text-emerald-700'
                                    : 'border-amber-300/60 bg-amber-50 text-amber-700',
                            )}>
                                {available ? t('proxyManager.coreAvailable') : t('proxyManager.coreUnavailable')}
                            </span>
                            <span className="text-[10px] text-muted-foreground tabular-nums">
                                {t('proxyManager.coreRunning', { count: Number(status?.running_instances) || 0 })}
                            </span>
                        </div>
                        <div className="mt-0.5 truncate text-xs text-muted-foreground" title={status?.binary_path || status?.error || ''}>
                            {status?.version || status?.error || t('proxyManager.coreAutoDetect')}
                        </div>
                    </div>
                </div>
                <div className="flex items-center gap-2">
                    <button type="button" onClick={onRefresh} className="btn btn-secondary btn-sm px-2" title={t('proxyManager.coreRefresh')}>
                        <RefreshCw className="h-3.5 w-3.5" />
                    </button>
                    <button type="button" onClick={onSave} disabled={loading} className="btn btn-primary btn-sm">
                        <Save className="h-3.5 w-3.5" />
                        {loading ? t('proxyManager.saving') : t('proxyManager.coreSave')}
                    </button>
                </div>
            </div>
            <div className="grid gap-3 px-4 py-3 md:grid-cols-[minmax(260px,1.4fr)_minmax(220px,1fr)_150px]">
                <div>
                    <label className="mb-1 block text-xs font-bold text-muted-foreground">{t('proxyManager.coreBinaryPath')}</label>
                    <input
                        type="text"
                        className="input-field h-9 min-h-9 text-xs"
                        value={form.xray_binary_path}
                        onChange={event => setForm({ ...form, xray_binary_path: event.target.value })}
                        placeholder={t('proxyManager.coreBinaryPlaceholder')}
                    />
                </div>
                <div>
                    <label className="mb-1 block text-xs font-bold text-muted-foreground">{t('proxyManager.coreRuntimeDir')}</label>
                    <input
                        type="text"
                        className="input-field h-9 min-h-9 text-xs"
                        value={form.runtime_dir}
                        onChange={event => setForm({ ...form, runtime_dir: event.target.value })}
                        placeholder={t('proxyManager.coreRuntimePlaceholder')}
                    />
                </div>
                <div>
                    <label className="mb-1 block text-xs font-bold text-muted-foreground">{t('proxyManager.coreStartupTimeout')}</label>
                    <input
                        type="number"
                        min="1"
                        max="60"
                        className="input-field h-9 min-h-9 text-xs"
                        value={form.startup_timeout_seconds}
                        onChange={event => setForm({ ...form, startup_timeout_seconds: Number(event.target.value) || 0 })}
                    />
                </div>
            </div>
        </div>
    )
}

function ProxyFormModal({
    show,
    t,
    form,
    setForm,
    editingProxy,
    loading,
    onClose,
    onSubmit,
}) {
    const isEditing = Boolean(editingProxy?.id)

    useEffect(() => {
        if (!show) {
            return undefined
        }
        const previousOverflow = document.body.style.overflow
        document.body.style.overflow = 'hidden'
        return () => {
            document.body.style.overflow = previousOverflow
        }
    }, [show])

    if (!show) {
        return null
    }
    const coreManaged = isCoreProxyType(form.type)

    const modal = (
        <div
            data-modal-overlay="proxy"
            className="fixed inset-0 z-[100] flex min-h-dvh items-center justify-center bg-slate-950/45 p-4 backdrop-blur-[3px] page-transition"
            onMouseDown={(event) => {
                if (event.target === event.currentTarget) {
                    onClose()
                }
            }}
        >
            <div
                role="dialog"
                aria-modal="true"
                className="ops-panel w-full max-w-lg max-h-[calc(100dvh-2rem)] overflow-y-auto page-transition"
            >
                <div className="p-4 border-b border-border flex justify-between items-center">
                    <div>
                        <h3 className="font-semibold">
                            {isEditing ? t('proxyManager.modalEditTitle') : t('proxyManager.modalAddTitle')}
                        </h3>
                        <p className="text-xs text-muted-foreground mt-1">
                            {t('proxyManager.modalDesc')}
                        </p>
                    </div>
                    <button type="button" onClick={onClose} className="text-muted-foreground hover:text-foreground">
                        <X className="w-5 h-5" />
                    </button>
                </div>

                <div className="p-6 space-y-4">
                    <div className="grid md:grid-cols-2 gap-4">
                        <div>
                            <label className="block text-sm font-medium mb-1.5">{t('proxyManager.nameLabel')}</label>
                            <input
                                type="text"
                                className="input-field"
                                placeholder={t('proxyManager.namePlaceholder')}
                                value={form.name}
                                onChange={e => setForm({ ...form, name: e.target.value })}
                            />
                        </div>
                        <div>
                            <label className="block text-sm font-medium mb-1.5">{t('proxyManager.typeLabel')}</label>
                            <select
                                className="input-field"
                                value={form.type}
                                onChange={e => setForm({ ...form, type: e.target.value })}
                            >
                                <option value="socks5">socks5</option>
                                <option value="socks5h">socks5h</option>
                                <option value="vless">VLESS</option>
                                <option value="vmess">VMess</option>
                                <option value="hysteria2">Hysteria2 / HY2</option>
                            </select>
                        </div>
                    </div>

                    {coreManaged ? (
                        <div>
                            <label className="block text-sm font-medium mb-1.5">{t('proxyManager.nodeUriLabel')}</label>
                            <input
                                type="password"
                                autoComplete="new-password"
                                className="input-field bg-card font-mono text-xs"
                                placeholder={t('proxyManager.nodeUriPlaceholder', { type: form.type })}
                                value={form.uri}
                                onChange={e => setForm({ ...form, uri: e.target.value })}
                            />
                            {isEditing && form.uri === '' && (
                                <p className="mt-1 text-[11px] text-muted-foreground">{t('proxyManager.nodeUriKeepHint')}</p>
                            )}
                        </div>
                    ) : (
                        <>
                            <div className="grid md:grid-cols-[1fr_128px] gap-4">
                                <div>
                                    <label className="block text-sm font-medium mb-1.5">{t('proxyManager.hostLabel')}</label>
                                    <input
                                        type="text"
                                        className="input-field"
                                        placeholder={t('proxyManager.hostPlaceholder')}
                                        value={form.host}
                                        onChange={e => setForm({ ...form, host: e.target.value })}
                                    />
                                </div>
                                <div>
                                    <label className="block text-sm font-medium mb-1.5">{t('proxyManager.portLabel')}</label>
                                    <input
                                        type="number"
                                        min="1"
                                        max="65535"
                                        className="input-field"
                                        value={form.port}
                                        onChange={e => setForm({ ...form, port: Number(e.target.value) || '' })}
                                    />
                                </div>
                            </div>

                            <div className="grid md:grid-cols-2 gap-4">
                                <div>
                                    <label className="block text-sm font-medium mb-1.5">{t('proxyManager.usernameLabel')}</label>
                                    <input
                                        type="text"
                                        className="input-field"
                                        placeholder={t('proxyManager.usernamePlaceholder')}
                                        value={form.username}
                                        onChange={e => setForm({ ...form, username: e.target.value })}
                                    />
                                </div>
                                <div>
                                    <label className="block text-sm font-medium mb-1.5">{t('proxyManager.passwordLabel')}</label>
                                    <input
                                        type="password"
                                        className="input-field bg-card"
                                        placeholder={t('proxyManager.passwordPlaceholder')}
                                        value={form.password}
                                        onChange={e => setForm({ ...form, password: e.target.value })}
                                    />
                                    {isEditing && (
                                        <p className="mt-1 text-[11px] text-muted-foreground">{t('proxyManager.passwordKeepHint')}</p>
                                    )}
                                </div>
                            </div>
                        </>
                    )}

                    <div className="rounded-lg border border-border bg-muted/20 px-3 py-2 text-xs text-muted-foreground">
                        {t(coreManaged ? 'proxyManager.coreTypeHelp' : 'proxyManager.typeHelp')}
                    </div>

                    <div className="flex justify-end gap-2 pt-2">
                        <button
                            type="button"
                            onClick={onClose}
                            className="btn btn-secondary"
                        >
                            {t('actions.cancel')}
                        </button>
                        <button
                            type="button"
                            onClick={onSubmit}
                            disabled={loading}
                            className="btn btn-primary"
                        >
                            {loading
                                ? t('proxyManager.saving')
                                : (isEditing ? t('proxyManager.saveEdit') : t('proxyManager.saveAdd'))}
                        </button>
                    </div>
                </div>
            </div>
        </div>
    )

    return createPortal(modal, document.body)
}

export default function ProxyManagerContainer({ config, onRefresh, onMessage, authFetch }) {
    const { t } = useI18n()
    const apiFetch = authFetch || fetch

    const [showModal, setShowModal] = useState(false)
    const [editingProxy, setEditingProxy] = useState(null)
    const [form, setForm] = useState(createEmptyProxyForm())
    const [saving, setSaving] = useState(false)
    const [testing, setTesting] = useState({})
    const [testResults, setTestResults] = useState({})
    const [coreStatus, setCoreStatus] = useState(null)
    const [coreForm, setCoreForm] = useState({ xray_binary_path: '', runtime_dir: '', startup_timeout_seconds: 10 })
    const [savingCore, setSavingCore] = useState(false)

    const proxies = config?.proxies || []

    const loadCoreStatus = async () => {
        try {
            const res = await apiFetch('/admin/proxies/core')
            const data = await readApiResponse(res, t('settings.nonJsonResponse', { status: res.status }))
            if (!res.ok) return
            setCoreStatus(data.status || null)
            setCoreForm({
                xray_binary_path: data.config?.xray_binary_path || '',
                runtime_dir: data.config?.runtime_dir || '',
                startup_timeout_seconds: Number(data.config?.startup_timeout_seconds) || 10,
            })
        } catch (_err) {
            setCoreStatus(null)
        }
    }

    useEffect(() => {
        loadCoreStatus()
    }, [])

    const openCreate = () => {
        setEditingProxy(null)
        setForm(createEmptyProxyForm())
        setShowModal(true)
    }

    const openEdit = (proxy) => {
        setEditingProxy(proxy)
        setForm({
            name: proxy.name || '',
            type: proxy.type || 'socks5h',
            host: proxy.host || '',
            port: proxy.port || 1080,
            username: proxy.username || '',
            password: '',
            uri: '',
        })
        setShowModal(true)
    }

    const closeModal = () => {
        setShowModal(false)
        setEditingProxy(null)
        setForm(createEmptyProxyForm())
    }

    const saveProxy = async () => {
        const coreManaged = isCoreProxyType(form.type)
        if ((!coreManaged && (!form.host || !form.port)) || (coreManaged && !form.uri && !editingProxy?.has_uri)) {
            onMessage('error', t('proxyManager.requiredFields'))
            return
        }
        setSaving(true)
        try {
            const url = editingProxy?.id
                ? `/admin/proxies/${encodeURIComponent(editingProxy.id)}`
                : '/admin/proxies'
            const method = editingProxy?.id ? 'PUT' : 'POST'
            const res = await apiFetch(url, {
                method,
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    name: form.name,
                    type: form.type,
                    host: form.host,
                    port: Number(form.port),
                    username: form.username,
                    password: form.password,
                    uri: form.uri,
                }),
            })
            const data = await readApiResponse(res, t('settings.nonJsonResponse', { status: res.status }))
            if (!res.ok) {
                onMessage('error', data.detail || t('messages.requestFailed'))
                return
            }
            await onRefresh?.()
            onMessage('success', editingProxy?.id ? t('proxyManager.updateSuccess') : t('proxyManager.addSuccess'))
            closeModal()
        } catch (err) {
            onMessage('error', err?.message || t('messages.networkError'))
        } finally {
            setSaving(false)
        }
    }

    const saveCoreSettings = async () => {
        setSavingCore(true)
        try {
            const res = await apiFetch('/admin/proxies/core', {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(coreForm),
            })
            const data = await readApiResponse(res, t('settings.nonJsonResponse', { status: res.status }))
            if (!res.ok) {
                onMessage('error', data.detail || t('messages.requestFailed'))
                return
            }
            setCoreStatus(data.status || null)
            await onRefresh?.()
            onMessage('success', t('proxyManager.coreSaved'))
        } catch (err) {
            onMessage('error', err?.message || t('messages.networkError'))
        } finally {
            setSavingCore(false)
        }
    }

    const deleteProxy = async (proxy) => {
        if (!confirm(t('proxyManager.deleteConfirm', { name: proxy.name || `${proxy.host}:${proxy.port}` }))) return
        try {
            const res = await apiFetch(`/admin/proxies/${encodeURIComponent(proxy.id)}`, { method: 'DELETE' })
            const data = await readApiResponse(res, t('settings.nonJsonResponse', { status: res.status }))
            if (!res.ok) {
                onMessage('error', data.detail || t('messages.deleteFailed'))
                return
            }
            await onRefresh?.()
            onMessage('success', t('messages.deleted'))
            setTestResults(prev => {
                const next = { ...prev }
                delete next[proxy.id]
                return next
            })
        } catch (err) {
            onMessage('error', err?.message || t('messages.networkError'))
        }
    }

    const testProxy = async (proxy) => {
        setTesting(prev => ({ ...prev, [proxy.id]: true }))
        try {
            const res = await apiFetch('/admin/proxies/test', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ proxy_id: proxy.id }),
            })
            const data = await readApiResponse(res, t('settings.nonJsonResponse', { status: res.status }))
            setTestResults(prev => ({ ...prev, [proxy.id]: data }))
            onMessage(data.success ? 'success' : 'error', data.message || t('messages.requestFailed'))
        } catch (err) {
            onMessage('error', err?.message || t('messages.networkError'))
        } finally {
            setTesting(prev => ({ ...prev, [proxy.id]: false }))
        }
    }

    return (
        <div className="space-y-6">
            <CoreStatusPanel
                t={t}
                status={coreStatus}
                form={coreForm}
                setForm={setCoreForm}
                loading={savingCore}
                onRefresh={loadCoreStatus}
                onSave={saveCoreSettings}
            />

            <div className="grid gap-4 md:grid-cols-3">
                <div className="metric-tile">
                    <div className="text-[10px] text-muted-foreground font-bold uppercase tracking-wider">{t('proxyManager.totalProxies')}</div>
                    <div className="mt-2 text-2xl font-bold">{proxies.length}</div>
                </div>
                <div className="metric-tile">
                    <div className="text-[10px] text-muted-foreground font-bold uppercase tracking-wider">{t('proxyManager.coreProxyCount')}</div>
                    <div className="mt-2 text-2xl font-bold">{proxies.filter(proxy => proxy.core_managed || isCoreProxyType(proxy.type)).length}</div>
                </div>
                <div className="metric-tile">
                    <div className="text-[10px] text-muted-foreground font-bold uppercase tracking-wider">{t('proxyManager.authProxyCount')}</div>
                    <div className="mt-2 text-2xl font-bold">{proxies.filter(proxy => proxy.username || proxy.has_password).length}</div>
                </div>
            </div>

            <ProxiesTable
                t={t}
                proxies={proxies}
                testing={testing}
                testResults={testResults}
                onCreate={openCreate}
                onTest={testProxy}
                onEdit={openEdit}
                onDelete={deleteProxy}
            />

            <ProxyFormModal
                show={showModal}
                t={t}
                form={form}
                setForm={setForm}
                editingProxy={editingProxy}
                loading={saving}
                onClose={closeModal}
                onSubmit={saveProxy}
            />
        </div>
    )
}
