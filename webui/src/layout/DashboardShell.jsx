import { Suspense, lazy, useCallback, useEffect, useMemo, useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import {
    Activity,
    Gauge,
    Globe,
    History,
    KeyRound,
    LayoutDashboard,
    Loader2,
    LogOut,
    Menu,
    Network,
    RadioTower,
    RefreshCw,
    Search,
    Settings as SettingsIcon,
    ShieldAlert,
    TerminalSquare,
    Upload,
    Users,
    X,
} from 'lucide-react'
import clsx from 'clsx'

import LanguageToggle from '../components/LanguageToggle'
import { useI18n } from '../i18n'

const AccountManagerContainer = lazy(() => import('../features/account/AccountManagerContainer'))
const ApiTesterContainer = lazy(() => import('../features/apiTester/ApiTesterContainer'))
const ChatHistoryContainer = lazy(() => import('../features/chatHistory/ChatHistoryContainer'))
const OverviewContainer = lazy(() => import('../features/overview/OverviewContainer'))
const BatchImport = lazy(() => import('../components/BatchImport'))
const SettingsContainer = lazy(() => import('../features/settings/SettingsContainer'))
const ProxyManagerContainer = lazy(() => import('../features/proxy/ProxyManagerContainer'))

const UPDATE_STATUS_REFRESH_INTERVAL_MS = 60_000
const VERSION_NOTIFY_STORAGE_KEY = 'deepseek-web-to-api_notified_update_tag'
const WEBUI_API_CONTRACT_VERSION = 3

function TabLoadingFallback({ label }) {
    return (
        <div className="ops-panel min-h-[360px] flex items-center justify-center">
            <div className="w-full max-w-sm space-y-4 px-8">
                <div className="flex items-center justify-center gap-3 text-sm font-semibold text-muted-foreground">
                    <Loader2 className="w-4 h-4 animate-spin text-primary" />
                    <span>{label}</span>
                </div>
                <div className="h-2 rounded-full skeleton-line" />
                <div className="mx-auto h-2 w-2/3 rounded-full skeleton-line" />
            </div>
        </div>
    )
}

export default function DashboardShell({ onLogout, authFetch, config, fetchConfig, showMessage, message, onForceLogout }) {
    const { t } = useI18n()
    const location = useLocation()
    const navigate = useNavigate()
    const [sidebarOpen, setSidebarOpen] = useState(false)
    const [versionInfo, setVersionInfo] = useState(null)
    const [versionCheckComplete, setVersionCheckComplete] = useState(false)
    const [updateStatus, setUpdateStatus] = useState(null)

    const navItems = [
        { id: 'overview', label: t('nav.overview.label'), icon: LayoutDashboard, description: t('nav.overview.desc') },
        { id: 'accounts', label: t('nav.accounts.label'), icon: Users, description: t('nav.accounts.desc') },
        { id: 'proxies', label: t('nav.proxies.label'), icon: Globe, description: t('nav.proxies.desc') },
        { id: 'test', label: t('nav.test.label'), icon: TerminalSquare, description: t('nav.test.desc') },
        { id: 'history', label: t('nav.history.label'), icon: History, description: t('nav.history.desc') },
        { id: 'import', label: t('nav.import.label'), icon: Upload, description: t('nav.import.desc') },
        { id: 'settings', label: t('nav.settings.label'), icon: SettingsIcon, description: t('nav.settings.desc') },
    ]

    const tabIds = new Set(navItems.map(item => item.id))
    const pathSegments = location.pathname.replace(/^\/+|\/+$/g, '').split('/').filter(Boolean)
    const routeSegments = pathSegments[0] === 'admin' ? pathSegments.slice(1) : pathSegments
    const pathTab = routeSegments[0] || ''
    const activeTab = tabIds.has(pathTab) ? pathTab : 'overview'
    const adminBasePath = pathSegments[0] === 'admin' ? '/admin' : ''
    const activeNavItem = navItems.find(n => n.id === activeTab)

    const queueConfig = config?.queue || config?.runtime?.queue || {}
    const accountCount = config.accounts?.length || 0
    const keyCount = config.api_keys?.length || config.keys?.length || 0
    const proxyCount = config.proxies?.length || 0

    const statusItems = useMemo(() => [
        { label: 'Health', value: 'Ready', icon: Activity, tone: 'ok' },
        { label: 'Accounts', value: accountCount, icon: Users, tone: accountCount > 0 ? 'ok' : 'warn' },
        { label: 'Keys', value: keyCount, icon: KeyRound, tone: keyCount > 0 ? 'ok' : 'warn' },
        { label: 'Proxies', value: proxyCount, icon: Network, tone: proxyCount > 0 ? 'ok' : 'warn' },
        { label: 'Queue', value: queueConfig.waiting ?? 0, icon: Gauge, tone: queueConfig.waiting > 0 ? 'warn' : 'ok' },
    ], [accountCount, keyCount, proxyCount, queueConfig.waiting])

    const navigateToTab = useCallback((tabID) => {
        const nextPath = tabID === 'overview'
            ? `${adminBasePath || ''}/`
            : `${adminBasePath}/${tabID}`
        navigate(nextPath)
        setSidebarOpen(false)
    }, [adminBasePath, navigate])

    useEffect(() => {
        let disposed = false
        async function loadVersion() {
            try {
                const res = await authFetch('/admin/version')
                const data = await res.json()
                if (!disposed) {
                    setVersionInfo(data)
                }
            } catch (_err) {
                if (!disposed) {
                    setVersionInfo(null)
                }
            } finally {
                if (!disposed) {
                    setVersionCheckComplete(true)
                }
            }
        }
        loadVersion()
        return () => {
            disposed = true
        }
    }, [authFetch])

    useEffect(() => {
        let disposed = false
        async function loadUpdateStatus() {
            try {
                const res = await authFetch('/admin/updates')
                if (!res.ok) return
                const data = await res.json()
                if (disposed) return
                setUpdateStatus(data)
                const latestTag = data?.status?.latest_tag
                if (data?.status?.update_available && latestTag) {
                    const notifiedTag = sessionStorage.getItem(VERSION_NOTIFY_STORAGE_KEY)
                    if (notifiedTag !== latestTag) {
                        sessionStorage.setItem(VERSION_NOTIFY_STORAGE_KEY, latestTag)
                        showMessage('success', t('sidebar.updateAvailableToast', { version: latestTag }))
                    }
                }
            } catch (_err) {
                // Update checks are owned by the server. A temporary status read
                // failure must not affect the rest of the admin console.
            }
        }
        loadUpdateStatus()
        const timer = window.setInterval(loadUpdateStatus, UPDATE_STATUS_REFRESH_INTERVAL_MS)
        return () => {
            disposed = true
            window.clearInterval(timer)
        }
    }, [authFetch, showMessage, t])

    const availableUpdate = updateStatus?.status?.update_available
        ? { tag: updateStatus.status.latest_tag }
        : null

    const serverAPIContractVersion = Number(versionInfo?.api_contract_version || 0)
    const hasAPIContractMismatch = versionCheckComplete
        && versionInfo !== null
        && serverAPIContractVersion < WEBUI_API_CONTRACT_VERSION

    const renderTab = () => {
        if (!versionCheckComplete) {
            return <TabLoadingFallback label={t('auth.checkingBackendCompatibility')} />
        }
        if (hasAPIContractMismatch) {
            return (
                <section className="ops-panel min-h-[360px] flex items-center justify-center p-6">
                    <div className="w-full max-w-xl text-center">
                        <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-lg border border-amber-200 bg-amber-50 text-amber-700">
                            <ShieldAlert className="h-6 w-6" />
                        </div>
                        <h2 className="mt-5 text-lg font-black text-slate-950">{t('auth.backendMismatchTitle')}</h2>
                        <p className="mt-2 text-sm leading-6 text-muted-foreground">
                            {t('auth.backendMismatchDescription', {
                                current: serverAPIContractVersion,
                                required: WEBUI_API_CONTRACT_VERSION,
                            })}
                        </p>
                        <button className="btn btn-primary mt-6" onClick={() => window.location.reload()}>
                            <RefreshCw className="h-4 w-4" />
                            {t('auth.reloadAfterUpgrade')}
                        </button>
                    </div>
                </section>
            )
        }
        switch (activeTab) {
            case 'overview':
                return <OverviewContainer config={config} onMessage={showMessage} authFetch={authFetch} />
            case 'accounts':
                return <AccountManagerContainer config={config} onRefresh={fetchConfig} onMessage={showMessage} authFetch={authFetch} />
            case 'proxies':
                return <ProxyManagerContainer config={config} onRefresh={fetchConfig} onMessage={showMessage} authFetch={authFetch} />
            case 'test':
                return <ApiTesterContainer config={config} onMessage={showMessage} authFetch={authFetch} />
            case 'history':
                return <ChatHistoryContainer onMessage={showMessage} authFetch={authFetch} />
            case 'import':
                return <BatchImport onRefresh={fetchConfig} onMessage={showMessage} authFetch={authFetch} />
            case 'settings':
                return <SettingsContainer onRefresh={fetchConfig} onMessage={showMessage} authFetch={authFetch} onForceLogout={onForceLogout} onUpdateStatus={setUpdateStatus} />
            default:
                return null
        }
    }

    return (
        <div className="ops-shell relative flex h-screen overflow-hidden bg-background text-foreground">
            {sidebarOpen && (
                <div
                    className="fixed inset-0 bg-slate-900/20 backdrop-blur-[2px] z-40 lg:hidden transition-opacity"
                    onClick={() => setSidebarOpen(false)}
                />
            )}

            <aside className={clsx(
                'fixed lg:static inset-y-0 left-0 z-50 w-[292px] border-r border-slate-200 bg-white/95 backdrop-blur-2xl transition-transform duration-300 ease-[cubic-bezier(0.2,0.8,0.2,1)] lg:transform-none flex flex-col shadow-[18px_0_42px_rgba(15,23,42,0.07)]',
                sidebarOpen ? 'translate-x-0' : '-translate-x-full',
            )}>
                <div className="px-5 pt-5 pb-4 border-b border-slate-200">
                    <div className="flex items-center gap-3">
                        <div className="min-w-0">
                            <div className="text-lg font-black leading-none text-slate-950">DeepSeek_Web_To_API</div>
                            <div className="text-[10px] mt-1 text-slate-500 font-bold uppercase">{t('sidebar.onlineAdminConsole')}</div>
                        </div>
                    </div>
                    <div className="mt-5 grid grid-cols-2 gap-2">
                        <div className="ops-mini-stat px-3 py-2">
                            <div className="text-[10px] text-blue-600 font-bold uppercase">Pool</div>
                            <div className="mt-0.5 text-2xl font-black tabular-nums text-slate-950">{accountCount}</div>
                        </div>
                        <div className="ops-mini-stat px-3 py-2">
                            <div className="text-[10px] text-emerald-700 font-bold uppercase">Keys</div>
                            <div className="mt-0.5 text-2xl font-black tabular-nums text-slate-950">{keyCount}</div>
                        </div>
                    </div>
                </div>

                <nav className="flex-1 px-3 py-4 space-y-1 overflow-y-auto custom-scrollbar">
                    {navItems.map((item) => {
                        const Icon = item.icon
                        const isActive = activeTab === item.id
                        return (
                            <button
                                key={item.id}
                                onClick={() => navigateToTab(item.id)}
                                className={clsx(
                                    'nav-item w-full grid grid-cols-[34px_1fr_auto] items-center gap-2 rounded-lg border px-2.5 py-2.5 text-left',
                                    isActive
                                        ? 'border-blue-200 bg-blue-50 text-blue-700 shadow-[inset_3px_0_0_rgba(0,110,255,0.9),0_10px_24px_rgba(0,110,255,0.08)]'
                                        : 'border-transparent text-slate-600 hover:border-blue-100 hover:bg-blue-50/70 hover:text-slate-950',
                                )}
                            >
                                <span className={clsx(
                                    'w-8 h-8 rounded-md flex items-center justify-center border transition-colors',
                                    isActive ? 'border-blue-200 bg-white text-blue-600' : 'border-slate-200 bg-slate-50 text-slate-500',
                                )}>
                                    <Icon className="w-4 h-4" />
                                </span>
                                <span className="min-w-0">
                                    <span className="block text-sm font-bold leading-tight">{item.label}</span>
                                    <span className="block mt-0.5 text-[11px] text-muted-foreground truncate">{item.description}</span>
                                </span>
                                {isActive && <span className="status-dot status-dot-ok" />}
                            </button>
                        )
                    })}
                </nav>

                <div className="p-4 border-t border-slate-200">
                    <div className="rounded-lg border border-slate-200 bg-slate-50/80 p-3 shadow-[inset_0_1px_0_rgba(255,255,255,0.8)]">
                        <div className="flex items-center justify-between">
                            <span className="text-[10px] text-muted-foreground font-bold uppercase">{t('sidebar.systemStatus')}</span>
                            <span className="inline-flex items-center gap-1.5 text-[10px] font-bold text-emerald-700">
                                <span className="w-1.5 h-1.5 rounded-full bg-emerald-500 animate-pulse" />
                                {t('sidebar.statusOnline')}
                            </span>
                        </div>
                        <div className="mt-3 flex items-center justify-between text-xs text-muted-foreground">
                            <span>{t('sidebar.version')}</span>
                            <span className="font-mono text-foreground">{versionInfo?.current_tag || '-'}</span>
                        </div>
                        {availableUpdate && (
                            <button
                                type="button"
                                className="version-update-link mt-3 w-full text-left"
                                onClick={() => navigateToTab('settings')}
                                aria-live="polite"
                            >
                                <span className="version-update-icon">
                                    <RefreshCw className="w-3.5 h-3.5" />
                                </span>
                                <span className="min-w-0 flex-1">
                                    <span className="block text-[11px] font-black leading-tight">{t('sidebar.updateAvailable')}</span>
                                    <span className="block mt-0.5 truncate font-mono text-[10px] opacity-80">{availableUpdate.tag}</span>
                                </span>
                            </button>
                        )}
                    </div>
                    <div className="mt-3 flex items-center gap-2">
                        <LanguageToggle />
                        <button
                            onClick={onLogout}
                            className="h-9 flex-1 inline-flex items-center justify-center gap-2 rounded-md border border-slate-200 bg-white text-xs font-bold text-slate-600 hover:bg-rose-50 hover:text-rose-700 hover:border-rose-200 transition-colors"
                        >
                            <LogOut className="w-3.5 h-3.5" />
                            {t('sidebar.signOut')}
                        </button>
                    </div>
                </div>
            </aside>

            <main className="flex-1 flex flex-col min-w-0 overflow-hidden">
                <header className="ops-header h-16 shrink-0 border-b border-slate-200 bg-white/85 backdrop-blur-2xl">
                    <div className="h-full px-4 lg:px-6 flex items-center justify-between gap-4">
                        <div className="flex items-center gap-3 min-w-0">
                            <button
                                onClick={() => setSidebarOpen(true)}
                                className="lg:hidden p-2 -ml-2 rounded-md text-muted-foreground hover:text-foreground hover:bg-cyan-300/10 transition-colors"
                                aria-label="打开导航"
                            >
                                <Menu className="w-5 h-5" />
                            </button>
                            <div className="hidden lg:flex w-9 h-9 rounded-lg border border-blue-100 bg-blue-50 items-center justify-center text-blue-600 shadow-[0_10px_24px_rgba(0,110,255,0.08)]">
                                <RadioTower className="w-4 h-4" />
                            </div>
                            <div className="min-w-0">
                                <div className="flex items-center gap-2">
                                    <h1 className="text-base lg:text-lg font-black truncate text-slate-950">{activeNavItem?.label}</h1>
                                    <span className="hidden sm:inline-flex rounded-md px-1.5 py-0.5 text-[10px] font-black uppercase bg-blue-50 text-blue-700 border border-blue-100">Ops</span>
                                </div>
                                <p className="hidden sm:block text-xs text-muted-foreground truncate">{activeNavItem?.description}</p>
                            </div>
                        </div>

                        <div className="hidden xl:flex items-center gap-2 flex-1 max-w-md">
                            <div className="relative w-full">
                                <Search className="input-icon-left absolute top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" />
                                <input
                                    className="input-field input-field-with-icon h-9"
                                    placeholder="搜索账号、代理、会话或日志"
                                    type="search"
                                />
                            </div>
                        </div>

                        <div className="flex items-center gap-2">
                            {statusItems.slice(0, 3).map(item => {
                                const Icon = item.icon
                                return (
                                    <div key={item.label} className="hidden md:flex items-center gap-2 h-9 rounded-lg border border-slate-200 bg-white px-2.5 transition-colors hover:border-blue-200 hover:bg-blue-50/70">
                                        <span className={clsx('status-dot', item.tone === 'warn' ? 'status-dot-warn' : 'status-dot-ok')} />
                                        <Icon className="w-3.5 h-3.5 text-muted-foreground" />
                                        <span className="text-[11px] font-bold text-muted-foreground uppercase">{item.label}</span>
                                        <span className="text-xs font-black tabular-nums">{item.value}</span>
                                    </div>
                                )
                            })}
                        </div>
                    </div>
                </header>

                <section className="hidden lg:block border-b border-slate-200 bg-white/50">
                    <div className="px-4 2xl:px-5 py-3 grid grid-cols-5 gap-3">
                        {statusItems.map(item => {
                            const Icon = item.icon
                            return (
                                <div key={item.label} className="ops-panel-muted px-3 py-2 flex items-center justify-between">
                                    <div>
                                        <div className="ops-kicker">{item.label}</div>
                                        <div className="mt-1 text-lg font-black tabular-nums">{item.value}</div>
                                    </div>
                                    <div className="w-9 h-9 rounded-lg border border-blue-100 bg-blue-50 flex items-center justify-center text-blue-600">
                                        <Icon className="w-4 h-4" />
                                    </div>
                                </div>
                            )
                        })}
                    </div>
                </section>

                <div className="relative flex-1 overflow-auto p-3 lg:p-4 2xl:p-5">
                    <div className="w-full space-y-4">
                        {message && (
                            <div className={clsx(
                                'toast-enter ops-panel px-4 py-3 flex items-center gap-3',
                                message.type === 'error' ? 'text-rose-700 border-rose-200 bg-rose-50' : 'text-blue-700 border-blue-200 bg-blue-50',
                            )}>
                                {message.type === 'error' ? <X className="w-4 h-4" /> : <Activity className="w-4 h-4" />}
                                <span className="text-sm font-semibold">{message.text}</span>
                            </div>
                        )}

                        <div key={activeTab} className="page-transition motion-stagger">
                            <Suspense fallback={<TabLoadingFallback label={activeNavItem?.label || 'DeepSeek_Web_To_API'} />}>
                                {renderTab()}
                            </Suspense>
                        </div>
                    </div>
                </div>
            </main>
        </div>
    )
}
