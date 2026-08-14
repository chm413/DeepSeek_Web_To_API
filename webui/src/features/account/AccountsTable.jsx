import { useState } from 'react'
import {
    Check,
    ChevronLeft,
    ChevronRight,
    Copy,
    FolderX,
    Loader2,
    Pencil,
    Plus,
    Power,
    RefreshCw,
    Search,
    Trash2,
    Upload,
} from 'lucide-react'
import clsx from 'clsx'

const accountGridColumns = 'grid-cols-[minmax(200px,1.15fr)_minmax(215px,1fr)_70px_110px_minmax(220px,1.25fr)_170px]'

function formatCompactTokens(value) {
    const count = Number(value) || 0
    if (count >= 1_000_000) return `${(count / 1_000_000).toFixed(1)}M`
    if (count >= 1_000) return `${(count / 1_000).toFixed(1)}K`
    return String(count)
}

function formatAccountCost(value, currency = 'USD') {
    const amount = Number(value) || 0
    const fractionDigits = amount > 0 && amount < 0.01 ? 6 : amount < 1 ? 4 : 2
    try {
        return new Intl.NumberFormat(undefined, {
            style: 'currency',
            currency,
            minimumFractionDigits: fractionDigits,
            maximumFractionDigits: fractionDigits,
        }).format(amount)
    } catch {
        return `$${amount.toFixed(fractionDigits)}`
    }
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
    const assigned = Number(proxy.assigned_account_count) || 0
    return `${name} | ${state} | ${latency} | ${region} | ${t('accountManager.proxyAssigned', { count: assigned })}`
}

function AccountProxySelector({ acc, identifier, proxies, autoRouteEnabled, updating, onUpdate, t }) {
    const assignedProxy = proxies.find(proxy => proxy.id === acc.proxy_id)
    const selection = acc.proxy_auto_route ? '__auto__' : (acc.proxy_id || '')
    const routeState = acc.proxy_auto_route
        ? assignedProxy?.route_available
            ? t('accountManager.proxyAutoActive')
            : t('accountManager.proxyAutoWaiting')
        : assignedProxy?.route_available
            ? t('accountManager.proxyAvailable')
            : assignedProxy
                ? t('accountManager.proxyUnavailable')
                : t('accountManager.proxyDirect')

    return (
        <div className="min-w-0">
            <select
                value={selection}
                title={assignedProxy ? proxyOptionLabel(assignedProxy, t) : routeState}
                onChange={event => {
                    const value = event.target.value
                    onUpdate(identifier, value === '__auto__' ? '' : value, value === '__auto__')
                }}
                disabled={updating}
                className="input-field h-8 min-h-8 py-1 text-xs"
            >
                <option value="">{t('accountManager.proxyNone')}</option>
                <option value="__auto__" disabled={!autoRouteEnabled || !acc.has_password}>
                    {t('accountManager.proxyAuto')}
                </option>
                {proxies.map(proxy => (
                    <option key={proxy.id} value={proxy.id}>
                        {proxyOptionLabel(proxy, t)}
                    </option>
                ))}
            </select>
            <div className="mt-1 flex min-w-0 flex-wrap items-center gap-1 text-[10px]">
                <span className={clsx(
                    'rounded border px-1.5 py-0.5 font-bold',
                    assignedProxy?.route_available
                        ? 'border-emerald-200 bg-emerald-50 text-emerald-700'
                        : acc.proxy_auto_route || assignedProxy
                            ? 'border-amber-200 bg-amber-50 text-amber-700'
                            : 'border-slate-200 bg-slate-50 text-slate-600',
                )}>
                    {routeState}
                </span>
                {assignedProxy && Number(assignedProxy.last_latency_ms) > 0 && <span>{assignedProxy.last_latency_ms}ms</span>}
                {assignedProxy && (assignedProxy.last_country || assignedProxy.last_colo) && (
                    <span>{[assignedProxy.last_country, assignedProxy.last_colo].filter(Boolean).join(' / ')}</span>
                )}
                {assignedProxy?.last_exit_ip && (
                    <span className="max-w-[150px] truncate font-mono" title={assignedProxy.last_exit_ip}>{assignedProxy.last_exit_ip}</span>
                )}
                {assignedProxy && <span>{t('accountManager.proxyAssigned', { count: Number(assignedProxy.assigned_account_count) || 0 })}</span>}
            </div>
        </div>
    )
}

function AccountUsageDetail({ acc, identifier, t }) {
    const usage = acc.token_usage_24h || {}
    const totalTokens = Number(usage.total_tokens) || 0
    const title = [
        `input=${Number(usage.input_tokens) || 0}`,
        `output=${Number(usage.output_tokens) || 0}`,
        `cache_hit=${Number(usage.cache_hit_input_tokens) || 0}`,
        `cache_miss=${Number(usage.cache_miss_input_tokens) || 0}`,
    ].join(' ')

    return (
        <div className="min-w-0" data-testid={`account-cost-${identifier}`} title={title}>
            <div className="truncate text-sm font-black tabular-nums text-amber-700">
                {formatAccountCost(usage.estimated_cost_usd, usage.currency || 'USD')}
            </div>
            <div className="mt-0.5 text-[10px] text-muted-foreground">
                {t('accountManager.usageWindow24h')} · {t('accountManager.usageTokens', { count: formatCompactTokens(totalTokens) })}
            </div>
        </div>
    )
}

function StatusBadge({ acc, isActive, runtimeUnknown, t }) {
    const disabled = acc.enabled === false
    const state = String(acc.account_state || '')
    const failed = acc.test_status === 'failed'
    const stateConfig = {
        permanently_banned: [t('accountManager.upstreamBanned'), 'danger'],
        invalid_credentials: [t('accountManager.invalidCredentials'), 'danger'],
        rate_limited: [t('accountManager.rateLimitedStatus'), 'warn'],
        temporarily_muted: [t('accountManager.mutedStatus'), 'warn'],
        disabled: [t('accountManager.manuallyDisabled'), 'neutral'],
        saturated: [t('accountManager.saturatedStatus'), 'warn'],
        busy: [t('accountManager.busyStatus'), 'info'],
        idle: [t('accountManager.idleStatus'), 'ok'],
    }
    const fallback = disabled
        ? [t('accountManager.manuallyDisabled'), 'neutral']
        : failed
            ? [t('accountManager.testStatusFailed'), 'danger']
            : isActive
                ? [t('accountManager.sessionActive'), 'ok']
                : runtimeUnknown
                    ? [t('accountManager.runtimeStatusUnknown'), 'info']
                    : [t('accountManager.reauthRequired'), 'warn']
    const [label, tone] = stateConfig[state] || fallback
    const toneClass = {
        danger: 'border-red-300/60 bg-red-50 text-red-700',
        warn: 'border-amber-300/60 bg-amber-50 text-amber-700',
        info: 'border-blue-300/60 bg-blue-50 text-blue-700',
        ok: 'border-emerald-300/60 bg-emerald-50 text-emerald-700',
        neutral: 'border-slate-300 bg-slate-100 text-slate-700',
    }[tone]

    return (
        <span className={clsx(
            'inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-[11px] font-bold',
            toneClass,
        )}>
            <span className={clsx(
                'status-dot',
                tone === 'danger' ? 'status-dot-danger' : tone === 'ok' ? 'status-dot-ok' : tone === 'neutral' ? 'bg-slate-400' : 'status-dot-warn',
            )} />
            {label}
        </span>
    )
}

function AccountRuntimeDetail({ acc, t }) {
    const maxInflight = Number(acc.max_inflight || 0)
    const inUse = Number(acc.in_use || 0)
    const availableSlots = Number(acc.available_slots || 0)
    const utilization = Math.max(0, Math.min(100, Number(acc.utilization_percent || 0)))
    const until = acc.health_until ? new Date(acc.health_until) : null
    if (maxInflight <= 0 && !until) return null

    return (
        <div className="mt-2 max-w-xs text-[10px] text-muted-foreground">
            {maxInflight > 0 && (
                <>
                    <div className="flex items-center justify-between gap-3 tabular-nums">
                        <span>{t('accountManager.runtimeSlots', { used: inUse, total: maxInflight })}</span>
                        <span>{t('accountManager.availableSlots', { count: availableSlots })}</span>
                    </div>
                    <div className="mt-1 h-1 overflow-hidden rounded-full bg-slate-200">
                        <div className="h-full bg-blue-500 transition-all" style={{ width: `${utilization}%` }} />
                    </div>
                </>
            )}
            {until && <div className="mt-1 text-amber-700">{t('accountManager.cooldownUntil', { time: until.toLocaleTimeString() })}</div>}
        </div>
    )
}

const accountTestPhaseTranslationKey = {
    token_refresh: 'accountManager.testPhaseTokenRefresh',
    token_refresh_retry: 'accountManager.testPhaseTokenRefreshRetry',
    session_create: 'accountManager.testPhaseSessionCreate',
    session_create_retry: 'accountManager.testPhaseSessionCreateRetry',
    model_validation: 'accountManager.testPhaseModelValidation',
    pow: 'accountManager.testPhasePow',
    completion: 'accountManager.testPhaseCompletion',
    complete: 'accountManager.testPhaseComplete',
    health_check: 'accountManager.testPhaseHealthCheck',
}

function accountTestPhaseLabel(phase, t) {
    return t(accountTestPhaseTranslationKey[phase] || 'accountManager.testPhaseUnknown')
}

function AccountCheckDetail({ acc, identifier, t }) {
    const result = acc?.test_result
    const healthReason = String(acc?.health_reason || '').trim()
    const failureReason = String(result?.failure_reason || healthReason).trim()
    const configWarning = String(result?.config_warning || '').trim()
    if (!result && !failureReason) return null

    const failed = result?.status === 'failed' || Boolean(failureReason)
    const phase = result?.phase || (healthReason ? 'health_check' : 'unknown')
    const errorCode = Number(result?.error_code)
    const httpStatus = Number(result?.http_status)

    if (!failed && !configWarning) {
        return (
            <p className="mt-1 text-[11px] text-emerald-700" data-testid={`account-check-${identifier}`}>
                {t('accountManager.lastCheckSuccess', { time: result?.response_time_ms || 0 })}
            </p>
        )
    }

    return (
        <div
            className="mt-2 max-w-full border-l-2 border-red-300 pl-2 text-[11px] leading-4 text-red-800"
            data-testid={`account-check-${identifier}`}
            role="status"
        >
            <div className="font-bold">{t('accountManager.lastCheckFailure', { phase: accountTestPhaseLabel(phase, t) })}</div>
            {failureReason && <div className="mt-0.5 break-words">{failureReason}</div>}
            {(errorCode > 0 || httpStatus > 0) && (
                <div className="mt-0.5 font-mono text-[10px] text-red-700">
                    {errorCode > 0 ? `code=${errorCode}` : ''}{errorCode > 0 && httpStatus > 0 ? ' ' : ''}{httpStatus > 0 ? `HTTP ${httpStatus}` : ''}
                </div>
            )}
            {configWarning && <div className="mt-1 break-words text-amber-700">{t('accountManager.lastCheckWarning', { warning: configWarning })}</div>}
        </div>
    )
}

export default function AccountsTable({
    t,
    accounts,
    loadingAccounts,
    testing,
    testingAll,
    batchProgress,
    sessionCounts,
    deletingSessions,
    updatingProxy,
    updatingEnabled,
    totalAccounts,
    page,
    pageSize,
    totalPages,
    resolveAccountIdentifier,
    proxies,
    autoRouteEnabled,
    onTestAll,
    onShowAddAccount,
    onShowBatchUpload,
    onEditAccount,
    onTestAccount,
    onDeleteAccount,
    onDeleteAllSessions,
    onUpdateAccountProxy,
    onUpdateAccountEnabled,
    onPrevPage,
    onNextPage,
    onPageSizeChange,
    searchQuery,
    onSearchChange,
    envBacked = false,
}) {
    const [copiedId, setCopiedId] = useState(null)
    const showBatchProgress = batchProgress.total > 0 && (testingAll || batchProgress.results.length > 0)
    const batchSuccessCount = batchProgress.results.filter(result => result.success).length

    const batchFailures = batchProgress.results.filter(result => !result.success && result.message)
    const batchProgressPercent = batchProgress.total > 0
        ? (batchProgress.current / batchProgress.total) * 100
        : 0

    const copyId = (id) => {
        navigator.clipboard.writeText(id).then(() => {
            setCopiedId(id)
            setTimeout(() => setCopiedId(null), 1500)
        })
    }

    return (
        <div className="ops-panel overflow-hidden">
            <div className="border-b border-border px-4 py-3">
                <div className="flex flex-col xl:flex-row xl:items-center justify-between gap-3">
                    <div>
                        <p className="ops-kicker">Managed Accounts</p>
                        <div className="mt-1 flex flex-wrap items-center gap-2">
                            <h2 className="ops-heading">{t('accountManager.accountsTitle')}</h2>
                            <span className="rounded-md border border-border bg-muted/60 px-2 py-0.5 text-[11px] font-black tabular-nums text-muted-foreground">
                                {totalAccounts}
                            </span>
                        </div>
                        <p className="ops-subtle mt-0.5">{t('accountManager.accountsDesc')}</p>
                    </div>
                    <div className="flex flex-col sm:flex-row gap-2 sm:items-center xl:min-w-[520px] 2xl:min-w-[680px]">
                        <div className="relative min-w-[260px] flex-1">
                            <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" />
                            <input
                                type="text"
                                value={searchQuery}
                                onChange={e => onSearchChange(e.target.value)}
                                placeholder={t('accountManager.searchPlaceholder')}
                                className="input-field pl-9"
                            />
                        </div>
                        <button
                            onClick={onTestAll}
                            disabled={testingAll || totalAccounts === 0}
                            className="btn btn-secondary"
                        >
                            {testingAll ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <RefreshCw className="w-3.5 h-3.5" />}
                            {t('accountManager.testAll')}
                        </button>
                        <button
                            onClick={onShowBatchUpload}
                            className="btn btn-secondary"
                        >
                            <Upload className="w-4 h-4" />
                            {t('accountManager.batchUploadButton')}
                        </button>
                        <button
                            onClick={onShowAddAccount}
                            className="btn btn-primary"
                        >
                            <Plus className="w-4 h-4" />
                            {t('accountManager.addAccount')}
                        </button>
                    </div>
                </div>
            </div>

            {showBatchProgress && (
                <div className="border-b border-border bg-blue-50/70 px-4 py-3 page-transition">
                    <div className="flex items-center justify-between text-sm mb-2">
                        <span className="font-bold">
                            {testingAll
                                ? t('accountManager.testingAllAccounts')
                                : t('accountManager.testAllCompleted', { success: batchSuccessCount, total: batchProgress.total })}
                        </span>
                        <span className="text-muted-foreground tabular-nums">{batchProgress.current} / {batchProgress.total}</span>
                    </div>
                    <div className="w-full bg-white rounded-full h-1.5 overflow-hidden mb-3 border border-blue-100">
                        <div
                            className="bg-primary h-full transition-all duration-300 ease-out"
                            style={{ width: `${batchProgressPercent}%` }}
                        />
                    </div>
                    {batchProgress.results.length > 0 && (
                        <>
                            <div className="grid grid-cols-2 md:grid-cols-4 xl:grid-cols-6 gap-2 max-h-28 overflow-y-auto custom-scrollbar">
                                {batchProgress.results.map((r, i) => (
                                    <div key={i} className={clsx(
                                        'text-xs px-2 py-1 rounded-md border truncate font-mono transition-transform hover:-translate-y-0.5',
                                        r.success ? 'bg-emerald-50 border-emerald-300/60 text-emerald-700' : 'bg-red-50 border-red-300/60 text-red-700',
                                    )} title={r.message || ''}>
                                        {r.success ? 'OK' : 'ERR'} {r.id}
                                    </div>
                                ))}
                            </div>
                            {batchFailures.length > 0 && (
                                <div className="mt-3 max-h-28 overflow-y-auto border-t border-red-200 pt-2 text-xs text-red-800 custom-scrollbar" role="status">
                                    {batchFailures.map((r, i) => (
                                        <div key={`${r.id}-${i}`} className="grid grid-cols-[minmax(110px,0.35fr)_minmax(0,1fr)] gap-2 py-1">
                                            <span className="truncate font-mono font-bold" title={r.id}>{r.id}</span>
                                            <span className="break-words">{r.message}</span>
                                        </div>
                                    ))}
                                </div>
                            )}
                        </>
                    )}
                </div>
            )}

            <div className="overflow-x-auto">
                <div className="min-w-[1080px]">
                    <div className={clsx('grid gap-3 border-b border-border bg-slate-50 px-4 py-2 text-[11px] font-black uppercase text-muted-foreground', accountGridColumns)}>
                        <div>{t('accountManager.accountColumn')}</div>
                        <div>{t('accountManager.statusColumn')}</div>
                        <div>{t('accountManager.sessionsColumn')}</div>
                        <div>{t('accountManager.usageColumn')}</div>
                        <div>{t('accountManager.proxyColumn')}</div>
                        <div className="text-right">{t('accountManager.actionsColumn')}</div>
                    </div>

                    {loadingAccounts ? (
                        <div className="px-4 py-5 space-y-3">
                            {[0, 1, 2, 3].map(i => (
                                <div key={i} className={clsx('grid gap-3 items-center', accountGridColumns)}>
                                    <div className="space-y-2">
                                        <div className="h-3 w-44 rounded-full skeleton-line" />
                                        <div className="h-2.5 w-64 rounded-full skeleton-line" />
                                    </div>
                                    <div className="h-7 rounded-full skeleton-line" />
                                    <div className="h-7 rounded-md skeleton-line" />
                                    <div className="h-7 rounded-md skeleton-line" />
                                    <div className="h-8 rounded-md skeleton-line" />
                                    <div className="h-8 rounded-md skeleton-line" />
                                </div>
                            ))}
                        </div>
                    ) : accounts.length > 0 ? (
                        accounts.map((acc, i) => {
                            const id = resolveAccountIdentifier(acc)
                            const runtimeUnknown = envBacked && !acc.test_status
                            const isActive = acc.test_status === 'ok' || acc.has_token
                            const accountUnavailable = acc.enabled === false || acc.account_state === 'permanently_banned' || acc.account_state === 'invalid_credentials'
                            const sessionCount = sessionCounts?.[id] ?? acc.session_count
                            return (
                                <div
                                    key={id || i}
                                    data-testid={`account-row-${id}`}
                                    className={clsx('page-transition table-row-hover grid gap-3 items-center border-b border-border/70 px-4 py-3 last:border-b-0', accountGridColumns)}
                                    style={{ animationDelay: `${Math.min(i, 10) * 18}ms` }}
                                >
                                    <div className="min-w-0">
                                        <div className="flex items-center gap-2">
                                            <span className={clsx(
                                                'status-dot',
                                                acc.test_status === 'failed' || accountUnavailable ? 'status-dot-danger' : isActive ? 'status-dot-ok' : 'status-dot-warn',
                                            )} />
                                            <span className="text-sm font-black truncate">{acc.name || '-'}</span>
                                        </div>
                                        <button
                                            className="mt-1 max-w-full inline-flex items-center gap-1.5 font-mono text-xs text-muted-foreground hover:text-primary transition-colors"
                                            onClick={() => copyId(id)}
                                        >
                                            <span className="truncate">{id || '-'}</span>
                                            {copiedId === id
                                                ? <Check className="w-3 h-3 text-emerald-600 shrink-0" />
                                                : <Copy className="w-3 h-3 opacity-50 shrink-0" />
                                            }
                                        </button>
                                        {acc.remark && (
                                            <div className="mt-1 text-xs text-muted-foreground truncate">{acc.remark}</div>
                                        )}
                                        {acc.token_preview && (
                                            <div className="mt-1 inline-flex font-mono bg-muted px-1.5 py-0.5 rounded text-[10px] text-muted-foreground">
                                                {acc.token_preview}
                                            </div>
                                        )}
                                    </div>

                                    <div className="min-w-0">
                                        <StatusBadge acc={acc} isActive={isActive} runtimeUnknown={runtimeUnknown} t={t} />
                                        <AccountRuntimeDetail acc={acc} t={t} />
                                        <AccountCheckDetail acc={acc} identifier={id} t={t} />
                                    </div>

                                    <div>
                                        {sessionCount !== undefined ? (
                                            <div className="flex items-center gap-2">
                                                <span className="rounded-md border border-blue-300/60 bg-blue-50 px-2 py-1 text-[11px] font-black text-blue-700 tabular-nums">
                                                    {t('accountManager.sessionCount', { count: sessionCount })}
                                                </span>
                                                {sessionCount > 0 && (
                                                    <button
                                                        onClick={() => onDeleteAllSessions(id)}
                                                        disabled={deletingSessions?.[id]}
                                                        className="p-1.5 rounded-md text-red-600 hover:bg-red-50 disabled:opacity-50 transition-colors"
                                                        title={t('accountManager.deleteAllSessions')}
                                                    >
                                                        {deletingSessions?.[id] ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <FolderX className="w-3.5 h-3.5" />}
                                                    </button>
                                                )}
                                            </div>
                                        ) : (
                                            <span className="text-xs text-muted-foreground">-</span>
                                        )}
                                    </div>

                                    <AccountUsageDetail acc={acc} identifier={id} t={t} />

                                    <AccountProxySelector
                                        acc={acc}
                                        identifier={id}
                                        proxies={proxies}
                                        autoRouteEnabled={autoRouteEnabled}
                                        updating={updatingProxy?.[id]}
                                        onUpdate={onUpdateAccountProxy}
                                        t={t}
                                    />

                                    <div className="flex items-center justify-end gap-1.5">
                                        <button
                                            type="button"
                                            role="switch"
                                            aria-checked={acc.enabled !== false}
                                            onClick={() => onUpdateAccountEnabled(id, acc.enabled === false)}
                                            disabled={!id || updatingEnabled?.[id]}
                                            className={clsx(
                                                'relative inline-flex h-7 w-12 shrink-0 items-center rounded-full border transition-colors disabled:opacity-50',
                                                acc.enabled !== false
                                                    ? 'border-emerald-400 bg-emerald-500'
                                                    : 'border-slate-300 bg-slate-200',
                                            )}
                                            title={acc.enabled !== false ? t('accountManager.disableAccount') : t('accountManager.enableAccount')}
                                        >
                                            <span className={clsx(
                                                'inline-flex h-5 w-5 items-center justify-center rounded-full bg-white shadow-sm transition-transform',
                                                acc.enabled !== false ? 'translate-x-6 text-emerald-600' : 'translate-x-1 text-slate-500',
                                            )}>
                                                {updatingEnabled?.[id]
                                                    ? <Loader2 className="h-3 w-3 animate-spin" />
                                                    : <Power className="h-3 w-3" />}
                                            </span>
                                        </button>
                                        <button
                                            onClick={() => onEditAccount(acc)}
                                            disabled={!id}
                                            className="btn btn-secondary btn-sm px-2"
                                            title={id ? t('accountManager.editAccountTitle') : t('accountManager.invalidIdentifier')}
                                        >
                                            <Pencil className="w-3.5 h-3.5" />
                                        </button>
                                        <button
                                            onClick={() => onTestAccount(id)}
                                            disabled={testing[id]}
                                            className="btn btn-secondary btn-sm px-2"
                                            title={testing[id] ? t('actions.testing') : t('actions.test')}
                                            aria-label={testing[id] ? t('actions.testing') : t('actions.test')}
                                        >
                                            {testing[id]
                                                ? <Loader2 className="w-3.5 h-3.5 animate-spin" />
                                                : <RefreshCw className="w-3.5 h-3.5" />}
                                        </button>
                                        <button
                                            onClick={() => onDeleteAccount(id)}
                                            className="btn btn-danger btn-sm px-2"
                                            title={t('actions.delete')}
                                        >
                                            <Trash2 className="w-3.5 h-3.5" />
                                        </button>
                                    </div>
                                </div>
                            )
                        })
                    ) : (
                        <div className="px-4 py-14 text-center">
                            <div className="mx-auto mb-3 flex h-11 w-11 items-center justify-center rounded-full border border-slate-200 bg-slate-50 text-muted-foreground">
                                <Search className="w-5 h-5" />
                            </div>
                            <div className="text-sm font-semibold text-muted-foreground">
                                {searchQuery ? t('accountManager.searchNoResults') : t('accountManager.noAccounts')}
                            </div>
                        </div>
                    )}
                </div>
            </div>

            {totalPages > 1 && (
                <div className="border-t border-border px-4 py-3 flex flex-col sm:flex-row sm:items-center justify-between gap-3 bg-muted/25">
                    <div className="flex items-center gap-3">
                        <div className="text-sm font-semibold text-muted-foreground">
                            {t('accountManager.pageInfo', { current: page, total: totalPages, count: totalAccounts })}
                        </div>
                        <select
                            value={pageSize}
                            onChange={e => onPageSizeChange(Number(e.target.value))}
                            className="input-field h-8 min-h-8 w-24 py-1 text-xs"
                        >
                            {[10, 20, 50, 100, 500, 1000, 2000, 5000].map(s => (
                                <option key={s} value={s}>{s}</option>
                            ))}
                        </select>
                    </div>
                    <div className="flex items-center gap-2">
                        <button
                            onClick={onPrevPage}
                            disabled={page <= 1 || loadingAccounts}
                            className="btn btn-secondary btn-sm px-2"
                        >
                            <ChevronLeft className="w-4 h-4" />
                        </button>
                        <span className="text-sm font-black px-2 tabular-nums">{page} / {totalPages}</span>
                        <button
                            onClick={onNextPage}
                            disabled={page >= totalPages || loadingAccounts}
                            className="btn btn-secondary btn-sm px-2"
                        >
                            <ChevronRight className="w-4 h-4" />
                        </button>
                    </div>
                </div>
            )}
        </div>
    )
}
