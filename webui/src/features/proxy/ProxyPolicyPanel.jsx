import { Save, ShieldCheck } from 'lucide-react'

export default function ProxyPolicyPanel({ t, policy, setPolicy, proxies, loading, onSave }) {
    return (
        <div className="ops-panel overflow-hidden">
            <div className="flex flex-col gap-3 border-b border-border px-4 py-3 md:flex-row md:items-center md:justify-between">
                <div className="flex items-center gap-3">
                    <div className="flex h-9 w-9 items-center justify-center rounded-md border border-emerald-200 bg-emerald-50 text-emerald-700">
                        <ShieldCheck className="h-4 w-4" />
                    </div>
                    <div>
                        <h2 className="text-sm font-black">{t('proxyManager.policyTitle')}</h2>
                        <p className="text-xs text-muted-foreground">{t('proxyManager.policyDesc')}</p>
                    </div>
                </div>
                <button type="button" className="btn btn-primary btn-sm" disabled={loading} onClick={onSave}>
                    <Save className="h-3.5 w-3.5" />
                    {loading ? t('proxyManager.saving') : t('proxyManager.policySave')}
                </button>
            </div>
            <div className="grid gap-4 px-4 py-4 md:grid-cols-2 xl:grid-cols-4">
                <label className="flex min-h-9 items-center gap-2 text-xs font-bold">
                    <input
                        type="checkbox"
                        checked={Boolean(policy.health_check_enabled)}
                        onChange={event => setPolicy({ ...policy, health_check_enabled: event.target.checked })}
                    />
                    {t('proxyManager.healthCheckEnabled')}
                </label>
                <label className="flex min-h-9 items-center gap-2 text-xs font-bold">
                    <input
                        type="checkbox"
                        checked={Boolean(policy.auto_enable_on_recovery)}
                        onChange={event => setPolicy({ ...policy, auto_enable_on_recovery: event.target.checked })}
                    />
                    {t('proxyManager.autoEnableRecovery')}
                </label>
                <Field label={t('proxyManager.healthInterval')}>
                    <input type="number" min="1" max="1440" className="input-field h-9 min-h-9 text-xs" value={policy.health_check_interval_minutes} onChange={event => setPolicy({ ...policy, health_check_interval_minutes: Number(event.target.value) || 0 })} />
                </Field>
                <Field label={t('proxyManager.failureThreshold')}>
                    <input type="number" min="1" max="100" className="input-field h-9 min-h-9 text-xs" value={policy.auto_disable_after_failures} onChange={event => setPolicy({ ...policy, auto_disable_after_failures: Number(event.target.value) || 0 })} />
                </Field>
                <Field label={t('proxyManager.subscriptionInterval')}>
                    <input type="number" min="5" max="10080" className="input-field h-9 min-h-9 text-xs" value={policy.subscription_update_interval_minutes} onChange={event => setPolicy({ ...policy, subscription_update_interval_minutes: Number(event.target.value) || 0 })} />
                </Field>
                <Field label={t('proxyManager.testConcurrency')}>
                    <input type="number" min="1" max="32" className="input-field h-9 min-h-9 text-xs" value={policy.test_concurrency} onChange={event => setPolicy({ ...policy, test_concurrency: Number(event.target.value) || 0 })} />
                </Field>
                <Field label={t('proxyManager.fallbackProxy')} className="md:col-span-2">
                    <select className="input-field h-9 min-h-9 text-xs" value={policy.fallback_proxy_id || ''} onChange={event => setPolicy({ ...policy, fallback_proxy_id: event.target.value })}>
                        <option value="">{t('proxyManager.fallbackDirect')}</option>
                        {proxies.filter(proxy => !proxy.disabled).map(proxy => (
                            <option key={proxy.id} value={proxy.id}>{proxy.name || `${proxy.host}:${proxy.port}`}</option>
                        ))}
                    </select>
                </Field>
            </div>
        </div>
    )
}

function Field({ label, className = '', children }) {
    return (
        <div className={className}>
            <label className="mb-1 block text-xs font-bold text-muted-foreground">{label}</label>
            {children}
        </div>
    )
}
