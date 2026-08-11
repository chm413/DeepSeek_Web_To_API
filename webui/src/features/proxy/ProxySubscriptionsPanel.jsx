import { useEffect, useState } from 'react'
import { createPortal } from 'react-dom'
import { Link2, Pencil, Plus, RefreshCw, Trash2, X } from 'lucide-react'
import clsx from 'clsx'

const EMPTY_FORM = {
    name: '',
    url: '',
    disabled: false,
    auto_update: true,
    auto_test: true,
    update_interval_minutes: 0,
    refresh: true,
}

function formatTime(value, t) {
    if (!value) return t('proxyManager.never')
    return new Date(Number(value) * 1000).toLocaleString()
}

export default function ProxySubscriptionsPanel({ t, subscriptions, busy, onCreate, onUpdate, onDelete, onRefresh, onRefreshAll }) {
    const [editing, setEditing] = useState(undefined)
    const [form, setForm] = useState(EMPTY_FORM)

    const openCreate = () => {
        setEditing(null)
        setForm({ ...EMPTY_FORM })
    }
    const openEdit = (subscription) => {
        setEditing(subscription)
        setForm({
            name: subscription.name || '',
            url: '',
            disabled: Boolean(subscription.disabled),
            auto_update: Boolean(subscription.auto_update),
            auto_test: Boolean(subscription.auto_test),
            update_interval_minutes: Number(subscription.update_interval_minutes) || 0,
            refresh: false,
        })
    }
    const close = () => {
        setEditing(undefined)
        setForm({ ...EMPTY_FORM })
    }
    const submit = async () => {
        const ok = editing
            ? await onUpdate(editing.id, form)
            : await onCreate(form)
        if (ok) close()
    }

    return (
        <div className="ops-panel overflow-hidden">
            <div className="flex flex-col gap-3 border-b border-border px-4 py-3 md:flex-row md:items-center md:justify-between">
                <div className="flex items-center gap-3">
                    <div className="flex h-9 w-9 items-center justify-center rounded-md border border-blue-200 bg-blue-50 text-blue-700">
                        <Link2 className="h-4 w-4" />
                    </div>
                    <div>
                        <h2 className="text-sm font-black">{t('proxyManager.subscriptionsTitle')}</h2>
                        <p className="text-xs text-muted-foreground">{t('proxyManager.subscriptionsDesc')}</p>
                    </div>
                </div>
                <div className="flex items-center gap-2">
                    <button type="button" className="btn btn-secondary btn-sm" disabled={Boolean(busy)} onClick={onRefreshAll}>
                        <RefreshCw className={clsx('h-3.5 w-3.5', busy === 'all' && 'animate-spin')} />
                        {t('proxyManager.refreshAllSubscriptions')}
                    </button>
                    <button type="button" className="btn btn-primary btn-sm" onClick={openCreate}>
                        <Plus className="h-3.5 w-3.5" />
                        {t('proxyManager.addSubscription')}
                    </button>
                </div>
            </div>
            {subscriptions.length === 0 ? (
                <div className="px-4 py-8 text-center text-sm text-muted-foreground">{t('proxyManager.noSubscriptions')}</div>
            ) : (
                <div className="divide-y divide-border">
                    {subscriptions.map(subscription => (
                        <div key={subscription.id} className="grid gap-3 px-4 py-3 lg:grid-cols-[minmax(180px,1fr)_minmax(260px,1.5fr)_auto] lg:items-center">
                            <div className="min-w-0">
                                <div className="flex flex-wrap items-center gap-2">
                                    <span className="truncate text-sm font-bold">{subscription.name}</span>
                                    <span className={clsx('rounded-full border px-2 py-0.5 text-[10px] font-bold', subscription.disabled ? 'border-slate-300 bg-slate-100 text-slate-600' : 'border-emerald-300 bg-emerald-50 text-emerald-700')}>
                                        {subscription.disabled ? t('proxyManager.subscriptionDisabled') : t('proxyManager.subscriptionActive')}
                                    </span>
                                    <span className="text-[10px] tabular-nums text-muted-foreground">{t('proxyManager.nodeCount', { count: subscription.node_count || 0 })}</span>
                                </div>
                                <div className="mt-1 text-[11px] text-muted-foreground">{t('proxyManager.lastUpdated', { time: formatTime(subscription.last_updated_at_unix, t) })}</div>
                            </div>
                            <div className="min-w-0 text-xs">
                                {subscription.last_error ? (
                                    <div className="truncate text-destructive" title={subscription.last_error}>{subscription.last_error}</div>
                                ) : (
                                    <div className="text-muted-foreground">
                                        {subscription.auto_update ? t('proxyManager.autoUpdateOn') : t('proxyManager.autoUpdateOff')}
                                        {' · '}
                                        {subscription.auto_test ? t('proxyManager.autoTestOn') : t('proxyManager.autoTestOff')}
                                    </div>
                                )}
                            </div>
                            <div className="flex items-center gap-1">
                                <button type="button" className="btn btn-secondary btn-sm" disabled={Boolean(busy)} onClick={() => onRefresh(subscription.id)} title={t('proxyManager.refreshSubscription')}>
                                    <RefreshCw className={clsx('h-3.5 w-3.5', busy === subscription.id && 'animate-spin')} />
                                </button>
                                <button type="button" className="p-2 text-muted-foreground hover:text-primary" onClick={() => openEdit(subscription)} title={t('proxyManager.editSubscription')}><Pencil className="h-4 w-4" /></button>
                                <button type="button" className="p-2 text-muted-foreground hover:text-destructive" onClick={() => onDelete(subscription)} title={t('proxyManager.deleteSubscription')}><Trash2 className="h-4 w-4" /></button>
                            </div>
                        </div>
                    ))}
                </div>
            )}
            <SubscriptionModal t={t} open={editing !== undefined} editing={editing} form={form} setForm={setForm} loading={busy === 'save'} onClose={close} onSubmit={submit} />
        </div>
    )
}

function SubscriptionModal({ t, open, editing, form, setForm, loading, onClose, onSubmit }) {
    useEffect(() => {
        if (!open) return undefined
        const previousOverflow = document.body.style.overflow
        document.body.style.overflow = 'hidden'
        return () => { document.body.style.overflow = previousOverflow }
    }, [open])
    if (!open) return null
    return createPortal(
        <div className="fixed inset-0 z-[100] flex min-h-dvh items-center justify-center bg-slate-950/45 p-4 backdrop-blur-[3px]" onMouseDown={event => event.target === event.currentTarget && onClose()}>
            <div role="dialog" aria-modal="true" className="ops-panel max-h-[calc(100dvh-2rem)] w-full max-w-lg overflow-y-auto">
                <div className="flex items-center justify-between border-b border-border p-4">
                    <div>
                        <h3 className="font-semibold">{editing ? t('proxyManager.editSubscription') : t('proxyManager.addSubscription')}</h3>
                        <p className="mt-1 text-xs text-muted-foreground">{t('proxyManager.subscriptionCredentialHint')}</p>
                    </div>
                    <button type="button" className="p-2 text-muted-foreground" onClick={onClose}><X className="h-5 w-5" /></button>
                </div>
                <div className="space-y-4 p-5">
                    <Field label={t('proxyManager.subscriptionName')}>
                        <input type="text" className="input-field" value={form.name} onChange={event => setForm({ ...form, name: event.target.value })} />
                    </Field>
                    <Field label={t('proxyManager.subscriptionUrl')}>
                        <input type="password" autoComplete="new-password" className="input-field font-mono text-xs" value={form.url} placeholder={editing ? t('proxyManager.subscriptionUrlKeep') : 'https://example.com/subscription'} onChange={event => setForm({ ...form, url: event.target.value })} />
                    </Field>
                    <div className="grid gap-3 sm:grid-cols-2">
                        <Check label={t('proxyManager.subscriptionEnabled')} checked={!form.disabled} onChange={value => setForm({ ...form, disabled: !value })} />
                        <Check label={t('proxyManager.autoUpdate')} checked={form.auto_update} onChange={value => setForm({ ...form, auto_update: value })} />
                        <Check label={t('proxyManager.autoTest')} checked={form.auto_test} onChange={value => setForm({ ...form, auto_test: value })} />
                        <Check label={t('proxyManager.refreshAfterSave')} checked={form.refresh} onChange={value => setForm({ ...form, refresh: value })} />
                    </div>
                    <Field label={t('proxyManager.subscriptionOwnInterval')}>
                        <input type="number" min="0" max="10080" className="input-field" value={form.update_interval_minutes} onChange={event => setForm({ ...form, update_interval_minutes: Number(event.target.value) || 0 })} />
                    </Field>
                    <div className="flex justify-end gap-2">
                        <button type="button" className="btn btn-secondary" onClick={onClose}>{t('actions.cancel')}</button>
                        <button type="button" className="btn btn-primary" disabled={loading || (!editing && !form.url)} onClick={onSubmit}>{loading ? t('proxyManager.saving') : t('proxyManager.saveEdit')}</button>
                    </div>
                </div>
            </div>
        </div>,
        document.body,
    )
}

function Field({ label, children }) {
    return <div><label className="mb-1.5 block text-sm font-medium">{label}</label>{children}</div>
}

function Check({ label, checked, onChange }) {
    return <label className="flex items-center gap-2 text-xs font-bold"><input type="checkbox" checked={checked} onChange={event => onChange(event.target.checked)} />{label}</label>
}
