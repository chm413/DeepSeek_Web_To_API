import { Ban, Gauge, HardDrive, LoaderCircle, ShieldAlert, Users } from 'lucide-react'

const toneStyles = {
    ok: 'border-emerald-100 bg-emerald-50 text-emerald-700',
    info: 'border-blue-100 bg-blue-50 text-blue-700',
    warn: 'border-amber-100 bg-amber-50 text-amber-700',
    danger: 'border-red-100 bg-red-50 text-red-700',
    neutral: 'border-slate-200 bg-slate-50 text-slate-700',
}

function QueueMetric({ icon: Icon, label, value, unit, tone = 'info' }) {
    return (
        <div className="metric-tile group min-w-0">
            <div className="flex items-start justify-between gap-2">
                <div className="min-w-0">
                    <p className="ops-kicker truncate">{label}</p>
                    <div className="mt-2 min-w-0">
                        <div className="truncate text-xl font-black tabular-nums text-foreground" title={String(value)}>{value}</div>
                        {unit && <div className="mt-0.5 truncate text-[10px] font-bold text-muted-foreground" title={String(unit)}>{unit}</div>}
                    </div>
                </div>
                <div className={`flex h-8 w-8 shrink-0 items-center justify-center rounded-md border ${toneStyles[tone]}`}>
                    <Icon className="h-4 w-4" />
                </div>
            </div>
        </div>
    )
}

export default function QueueCards({ queueStatus, metrics, t }) {
    if (!queueStatus) return null

    const counts = queueStatus.state_counts || {}
    const cache = metrics?.cache || {}
    const cacheRate = Number(cache.hit_rate || 0)
    const busyCount = Number(counts.busy || 0) + Number(counts.saturated || 0)
    const slotLimit = Number(queueStatus.global_max_inflight || queueStatus.recommended_concurrency || 0)
    const collectedAt = Number(metrics?.collected_at || 0)

    return (
        <div className="ops-panel p-3">
            <div className="mb-3 flex flex-wrap items-end justify-between gap-2 px-1">
                <div>
                    <p className="ops-kicker">Account Pool</p>
                    <h2 className="ops-heading mt-1">{t('accountManager.runtimeOverview')}</h2>
                </div>
                <div className="text-[11px] font-semibold text-muted-foreground tabular-nums">
                    {t('accountManager.runtimeUpdated', {
                        time: collectedAt ? new Date(collectedAt).toLocaleTimeString() : '-',
                    })}
                </div>
            </div>
            <div className="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6">
                <QueueMetric icon={Users} label={t('accountManager.idleStatus')} value={counts.idle || 0} unit={t('accountManager.accountsUnit')} tone="ok" />
                <QueueMetric icon={LoaderCircle} label={t('accountManager.busyStatus')} value={busyCount} unit={t('accountManager.accountsUnit')} tone={busyCount > 0 ? 'warn' : 'neutral'} />
                <QueueMetric icon={Gauge} label={t('accountManager.slotUsage')} value={`${queueStatus.in_use || 0}/${slotLimit}`} unit={t('accountManager.threadsUnit')} tone={queueStatus.in_use > 0 ? 'info' : 'neutral'} />
                <QueueMetric icon={ShieldAlert} label={t('accountManager.rateLimitedStatus')} value={counts.rate_limited || 0} unit={t('accountManager.accountsUnit')} tone={counts.rate_limited > 0 ? 'warn' : 'neutral'} />
                <QueueMetric icon={Ban} label={t('accountManager.bannedStatus')} value={counts.permanently_banned || 0} unit={t('accountManager.accountsUnit')} tone={counts.permanently_banned > 0 ? 'danger' : 'neutral'} />
                <QueueMetric icon={HardDrive} label={t('accountManager.globalCacheHitRate')} value={`${cacheRate.toFixed(2)}%`} unit={`${cache.hits || 0}/${cache.lookups || 0}`} tone="info" />
            </div>
            <div className="mt-3 flex flex-wrap gap-x-4 gap-y-1 border-t border-border/70 px-1 pt-2 text-[11px] font-semibold text-muted-foreground">
                <span>{t('accountManager.totalPool')}: <b className="text-foreground">{queueStatus.total || 0}</b></span>
                <span>{t('accountManager.available')}: <b className="text-foreground">{queueStatus.available || 0}</b></span>
                <span>{t('accountManager.mutedStatus')}: <b className="text-foreground">{counts.temporarily_muted || 0}</b></span>
                <span>{t('accountManager.invalidCredentialsShort')}: <b className="text-foreground">{counts.invalid_credentials || 0}</b></span>
                <span>{t('accountManager.manuallyDisabled')}: <b className="text-foreground">{counts.disabled || 0}</b></span>
                <span>{t('accountManager.waitingRequests')}: <b className="text-foreground">{queueStatus.waiting || 0}</b></span>
            </div>
        </div>
    )
}
