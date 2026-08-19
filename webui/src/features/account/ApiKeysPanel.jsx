import { useState } from 'react'
import { Check, ChevronDown, Copy, Pencil, Plus, Trash2 } from 'lucide-react'
import clsx from 'clsx'

import { maskSecret } from '../../utils/maskSecret'

function fallbackCopyText(text) {
    const textArea = document.createElement('textarea')
    textArea.value = text
    textArea.setAttribute('readonly', '')
    textArea.style.position = 'fixed'
    textArea.style.top = '-9999px'
    textArea.style.left = '-9999px'

    document.body.appendChild(textArea)
    textArea.focus()
    textArea.select()

    let copied = false
    try {
        copied = document.execCommand('copy')
    } finally {
        document.body.removeChild(textArea)
    }

    if (!copied) {
        throw new Error('copy failed')
    }
}

export default function ApiKeysPanel({
    t,
    config,
    keysExpanded,
    setKeysExpanded,
    onAddKey,
    onEditKey,
    copiedKey,
    setCopiedKey,
    onDeleteKey,
}) {
    const [failedKey, setFailedKey] = useState(null)
    const apiKeys = Array.isArray(config?.api_keys) && config.api_keys.length > 0
        ? config.api_keys
        : (config?.keys || []).map(key => ({ key, name: '', remark: '' }))

    const handleCopyKey = async (key) => {
        if (!key) return
        try {
            if (navigator.clipboard?.writeText) {
                await navigator.clipboard.writeText(key)
            } else {
                fallbackCopyText(key)
            }
            setCopiedKey(key)
            setFailedKey(null)
            setTimeout(() => setCopiedKey(null), 2000)
        } catch {
            try {
                fallbackCopyText(key)
                setCopiedKey(key)
                setFailedKey(null)
                setTimeout(() => setCopiedKey(null), 2000)
            } catch {
                setFailedKey(key)
                setTimeout(() => setFailedKey(null), 2500)
            }
        }
    }

    return (
        <div className="ops-panel overflow-hidden">
            <div
                className="px-4 py-3 flex flex-col md:flex-row md:items-center justify-between gap-3 cursor-pointer select-none hover:bg-blue-50/50 transition-colors border-b border-border"
                onClick={() => setKeysExpanded(!keysExpanded)}
            >
                <div className="flex items-center gap-3">
                    <ChevronDown className={clsx(
                        "w-4 h-4 text-muted-foreground transition-transform duration-200",
                        keysExpanded ? "rotate-0" : "-rotate-90"
                    )} />
                    <div>
                        <p className="ops-kicker">Access Control</p>
                        <h2 className="ops-heading mt-1">{t('accountManager.apiKeysTitle')}</h2>
                        <p className="ops-subtle mt-0.5">{t('accountManager.apiKeysDesc')} ({apiKeys.length || 0})</p>
                    </div>
                </div>
                <button
                    onClick={(e) => { e.stopPropagation(); onAddKey() }}
                    className="btn btn-primary"
                >
                    <Plus className="w-4 h-4" />
                    {t('accountManager.addKey')}
                </button>
            </div>

            {keysExpanded && (
                <div className="divide-y divide-border page-transition">
                    {apiKeys.length > 0 ? (
                        apiKeys.map((item, i) => (
                            <div key={i} className="px-4 py-3 flex items-center justify-between hover:bg-blue-50/45 transition-colors group">
                                <div className="grid grid-cols-1 md:grid-cols-[minmax(120px,180px)_minmax(220px,1fr)_minmax(120px,1fr)] gap-2 flex-1 items-center">
                                    <div className="text-sm font-bold">{item.name || '-'}</div>
                                    {item.key ? (
                                        <button
                                            onClick={() => handleCopyKey(item.key)}
                                            className="font-mono text-xs bg-slate-50 border border-border px-3 py-1.5 rounded-md inline-block hover:border-blue-200 hover:bg-white hover:text-primary transition-colors text-left"
                                            title={t('accountManager.copyKeyTitle')}
                                        >
                                            {maskSecret(item.key)}
                                        </button>
                                    ) : (
                                        <span className="font-mono text-xs text-muted-foreground">
                                            {item.key_preview || '********'}
                                        </span>
                                    )}
                                    <div className="text-sm text-muted-foreground truncate">{item.remark || '-'}</div>
                                    {copiedKey === item.key && (
                                        <span className="text-xs text-green-500 animate-pulse">{t('accountManager.copied')}</span>
                                    )}
                                    {failedKey === item.key && (
                                        <span className="text-xs text-destructive">{t('accountManager.copyFailed')}</span>
                                    )}
                                </div>
                                <div className="flex items-center gap-1 pl-3">
                                    <button
                                        onClick={() => onEditKey(item)}
                                        className="p-2 text-muted-foreground hover:text-primary hover:bg-blue-50 rounded-md transition-colors"
                                        title={t('accountManager.editKeyTitle')}
                                    >
                                        <Pencil className="w-4 h-4" />
                                    </button>
                                    <button
                                        onClick={() => handleCopyKey(item.key)}
                                        disabled={!item.key}
                                        className="p-2 text-muted-foreground hover:text-primary hover:bg-blue-50 rounded-md transition-colors"
                                        title={t('accountManager.copyKeyTitle')}
                                    >
                                        {copiedKey === item.key ? <Check className="w-4 h-4 text-green-500" /> : <Copy className="w-4 h-4" />}
                                    </button>
                                    <button
                                        onClick={() => onDeleteKey(item)}
                                        className="p-2 text-muted-foreground hover:text-destructive hover:bg-destructive/10 rounded-md transition-colors"
                                        title={t('accountManager.deleteKeyTitle')}
                                    >
                                        <Trash2 className="w-4 h-4" />
                                    </button>
                                </div>
                            </div>
                        ))
                    ) : (
                        <div className="p-8 text-center text-sm text-muted-foreground">{t('accountManager.noApiKeys')}</div>
                    )}
                </div>
            )}
        </div>
    )
}
