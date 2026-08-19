import { useState } from 'react'
import { Download, Upload, Copy, Check, AlertTriangle, ListPlus } from 'lucide-react'
import clsx from 'clsx'
import { useI18n } from '../i18n'

export default function BatchImport({ onRefresh, onMessage, authFetch }) {
    const { t } = useI18n()
    const [accountInput, setAccountInput] = useState('')
    const [loading, setLoading] = useState(false)
    const [result, setResult] = useState(null)
    const [copied, setCopied] = useState(false)

    const apiFetch = authFetch || fetch

    const handleImport = async () => {
        const raw = accountInput.trim()
        if (!raw) {
            onMessage('error', t('batchImport.enterAccountsText'))
            return
        }

        setLoading(true)
        setResult(null)
        try {
            const res = await apiFetch('/admin/import', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ accounts_text: raw }),
            })
            const data = await res.json()
            if (res.ok) {
                setResult(data)
                onMessage('success', t('batchImport.importSuccess', { keys: data.imported_keys, accounts: data.imported_accounts }))
                onRefresh()
            } else {
                onMessage('error', data.detail || t('messages.importFailed'))
            }
        } catch (e) {
            onMessage('error', t('messages.networkError'))
        } finally {
            setLoading(false)
        }
    }

    const copyBase64 = async () => {
        try {
            const res = await apiFetch('/admin/export')
            if (res.ok) {
                const data = await res.json()
                await navigator.clipboard.writeText(data.base64)
                setCopied(true)
                setTimeout(() => setCopied(false), 2000)
                onMessage('success', t('batchImport.copySuccess'))
            }
        } catch (e) {
            onMessage('error', t('messages.copyFailed'))
        }
    }

    return (
        <div className="flex flex-col lg:grid lg:grid-cols-3 gap-6 lg:h-[calc(100vh-140px)]">
            <div className="md:col-span-1 space-y-4">
                <div className="ops-panel p-5">
                    <h3 className="font-semibold flex items-center gap-2 mb-4">
                        <ListPlus className="w-4 h-4 text-primary" />
                        {t('batchImport.plainAccountsTitle')}
                    </h3>
                    <div className="rounded-lg border border-blue-100 bg-blue-50/70 p-3 text-xs leading-6 text-slate-700">
                        <div className="font-black text-blue-700">{t('batchImport.plainAccountsFormat')}</div>
                        <code className="mt-2 block rounded-md border border-blue-100 bg-white px-2 py-1 text-[11px] text-slate-700">
                            user@example.com:password123<br />
                            13800000000:password123
                        </code>
                        <p className="mt-2">{t('batchImport.plainAccountsDesc')}</p>
                    </div>
                </div>

                <div className="ops-panel p-5 bg-gradient-to-br from-blue-50 to-white">
                    <h3 className="font-semibold flex items-center gap-2 mb-2 text-primary">
                        <Download className="w-4 h-4" />
                        {t('batchImport.dataExport')}
                    </h3>
                    <p className="text-sm text-muted-foreground mb-4">
                        {t('batchImport.dataExportDesc')}
                    </p>
                    <button
                        onClick={copyBase64}
                        className="btn btn-primary w-full py-2.5 text-sm"
                    >
                        {copied ? <Check className="w-4 h-4" /> : <Copy className="w-4 h-4" />}
                        {copied ? t('batchImport.copied') : t('batchImport.copyBase64')}
                    </button>
                    <p className="text-[10px] text-muted-foreground mt-2 text-center">
                        {t('batchImport.variableName')}: <code className="bg-background px-1 py-0.5 rounded border border-border">DEEPSEEK_WEB_TO_API_CONFIG_JSON</code>
                    </p>
                </div>
            </div>

            <div className="lg:col-span-2 flex flex-col ops-panel overflow-hidden min-h-[400px] lg:h-full">
                    <div className="p-4 border-b border-border flex items-center justify-between bg-slate-50">
                    <h3 className="font-semibold flex items-center gap-2">
                        <Upload className="w-4 h-4 text-primary" />
                        {t('batchImport.accountsEditor')}
                    </h3>
                    <div className="flex gap-2">
                        <button onClick={handleImport} disabled={loading} className="btn btn-primary btn-sm">
                            {loading ? t('batchImport.importing') : t('batchImport.applyAccounts')}
                        </button>
                    </div>
                </div>

                <div className="flex-1 relative min-h-[400px]">
                    <textarea
                        className="absolute inset-0 w-full h-full p-4 font-mono text-sm bg-card text-foreground resize-none focus:outline-none custom-scrollbar"
                        value={accountInput}
                        onChange={e => setAccountInput(e.target.value)}
                        placeholder={'user@example.com:password123\n13800000000:password123'}
                        spellCheck={false}
                    />
                </div>

                {result && (
                    <div className={clsx(
                        "p-4 border-t",
                        result.imported_keys || result.imported_accounts ? "bg-emerald-500/10 border-emerald-500/20" : "bg-destructive/10 border-destructive/20"
                    )}>
                        <div className="flex items-start gap-3">
                            {result.imported_keys || result.imported_accounts ? (
                                <Check className="w-5 h-5 text-emerald-500 mt-0.5" />
                            ) : (
                                <AlertTriangle className="w-5 h-5 text-destructive mt-0.5" />
                            )}
                            <div>
                                <h4 className={clsx("font-medium", result.imported_keys || result.imported_accounts ? "text-emerald-500" : "text-destructive")}>
                                    {t('batchImport.importComplete')}
                                </h4>
                                <p className="text-sm opacity-80 mt-1">
                                    {t('batchImport.importSummary', { keys: result.imported_keys, accounts: result.imported_accounts })}
                                </p>
                                {result.message && (
                                    <p className="text-xs opacity-70 mt-1">{result.message}</p>
                                )}
                                {(result.duplicate_accounts > 0 || result.invalid_accounts > 0 || result.skipped_keys > 0) && (
                                    <p className="text-xs opacity-60 mt-1">
                                        {result.duplicate_accounts > 0 && `已存在 ${result.duplicate_accounts} 个账号`}
                                        {result.duplicate_accounts > 0 && result.invalid_accounts > 0 && ' · '}
                                        {result.invalid_accounts > 0 && `无效 ${result.invalid_accounts} 个账号`}
                                        {(result.duplicate_accounts > 0 || result.invalid_accounts > 0) && result.skipped_keys > 0 && ' · '}
                                        {result.skipped_keys > 0 && `已存在 ${result.skipped_keys} 个 Key`}
                                    </p>
                                )}
                            </div>
                        </div>
                    </div>
                )}
            </div>
        </div>
    )
}
