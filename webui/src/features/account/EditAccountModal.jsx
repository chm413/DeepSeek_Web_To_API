import { X } from 'lucide-react'

export default function EditAccountModal({
    show,
    t,
    editingAccount,
    editAccount,
    setEditAccount,
    loading,
    onClose,
    onSave,
}) {
    if (!show || !editingAccount) {
        return null
    }

    return (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/45 backdrop-blur-sm p-4 animate-in fade-in">
            <div className="ops-panel w-full max-w-md overflow-hidden animate-in zoom-in-95">
                <div className="p-4 border-b border-border flex justify-between items-start gap-4">
                    <div className="min-w-0">
                        <h3 className="font-black">{t('accountManager.modalEditAccountTitle')}</h3>
                        <p className="mt-1 text-xs text-muted-foreground">{t('accountManager.editAccountHint')}</p>
                    </div>
                    <button onClick={onClose} className="text-muted-foreground hover:text-foreground">
                        <X className="w-5 h-5" />
                    </button>
                </div>
                <div className="p-6 space-y-4">
                    <div className="rounded-md border border-border bg-muted/20 px-3 py-2">
                        <div className="text-xs font-medium text-muted-foreground mb-1">{t('accountManager.accountIdentifierLabel')}</div>
                        <code className="text-sm font-mono text-foreground break-all">{editingAccount.identifier}</code>
                    </div>
                    <div>
                        <label className="block text-sm font-medium mb-1.5">{t('accountManager.nameOptional')}</label>
                        <input
                            type="text"
                            className="input-field"
                            placeholder={t('accountManager.namePlaceholder')}
                            value={editAccount.name}
                            onChange={e => setEditAccount({ ...editAccount, name: e.target.value })}
                            autoFocus
                        />
                    </div>
                    <div>
                        <label className="block text-sm font-medium mb-1.5">{t('accountManager.remarkOptional')}</label>
                        <input
                            type="text"
                            className="input-field"
                            placeholder={t('accountManager.remarkPlaceholder')}
                            value={editAccount.remark}
                            onChange={e => setEditAccount({ ...editAccount, remark: e.target.value })}
                        />
                    </div>
                    <label className="flex items-center justify-between gap-4 rounded-md border border-border bg-muted/20 px-3 py-2.5">
                        <span>
                            <span className="block text-sm font-medium">{t('accountManager.accountEnabledLabel')}</span>
                            <span className="block text-xs text-muted-foreground">{t('accountManager.accountEnabledHint')}</span>
                        </span>
                        <input
                            type="checkbox"
                            checked={editAccount.enabled !== false}
                            onChange={e => setEditAccount({ ...editAccount, enabled: e.target.checked })}
                            className="h-4 w-4 accent-emerald-600"
                        />
                    </label>
                    <div className="flex justify-end gap-2 pt-2">
                        <button onClick={onClose} className="btn btn-secondary">{t('actions.cancel')}</button>
                        <button onClick={onSave} disabled={loading} className="btn btn-primary">
                            {loading ? t('accountManager.editAccountLoading') : t('accountManager.editAccountAction')}
                        </button>
                    </div>
                </div>
            </div>
        </div>
    )
}
