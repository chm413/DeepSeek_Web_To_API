import { X } from 'lucide-react'

export default function AddAccountModal({
    show,
    t,
    newAccount,
    setNewAccount,
    loading,
    onClose,
    onAdd,
}) {
    if (!show) {
        return null
    }

    return (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/45 backdrop-blur-sm p-4 animate-in fade-in">
            <div className="ops-panel w-full max-w-md overflow-hidden animate-in zoom-in-95">
                <div className="p-4 border-b border-border flex justify-between items-center">
                    <h3 className="font-black">{t('accountManager.modalAddAccountTitle')}</h3>
                    <button onClick={onClose} className="text-muted-foreground hover:text-foreground">
                        <X className="w-5 h-5" />
                    </button>
                </div>
                <div className="p-6 space-y-4">
                    <div>
                        <label className="block text-sm font-medium mb-1.5">{t('accountManager.nameOptional')}</label>
                        <input
                            type="text"
                            className="input-field"
                            placeholder={t('accountManager.namePlaceholder')}
                            value={newAccount.name}
                            onChange={e => setNewAccount({ ...newAccount, name: e.target.value })}
                        />
                    </div>
                    <div>
                        <label className="block text-sm font-medium mb-1.5">{t('accountManager.remarkOptional')}</label>
                        <input
                            type="text"
                            className="input-field"
                            placeholder={t('accountManager.remarkPlaceholder')}
                            value={newAccount.remark}
                            onChange={e => setNewAccount({ ...newAccount, remark: e.target.value })}
                        />
                    </div>
                    <div>
                        <label className="block text-sm font-medium mb-1.5">{t('accountManager.emailOptional')}</label>
                        <input
                            type="email"
                            className="input-field"
                            placeholder="user@example.com"
                            value={newAccount.email}
                            onChange={e => setNewAccount({ ...newAccount, email: e.target.value })}
                        />
                    </div>
                    <div>
                        <label className="block text-sm font-medium mb-1.5">{t('accountManager.mobileOptional')}</label>
                        <input
                            type="text"
                            className="input-field"
                            placeholder="+86..."
                            value={newAccount.mobile}
                            onChange={e => setNewAccount({ ...newAccount, mobile: e.target.value })}
                        />
                    </div>
                    <div>
                        <label className="block text-sm font-medium mb-1.5">{t('accountManager.passwordLabel')} <span className="text-destructive">*</span></label>
                        <input
                            type="password"
                            className="input-field bg-card"
                            placeholder={t('accountManager.passwordPlaceholder')}
                            value={newAccount.password}
                            onChange={e => setNewAccount({ ...newAccount, password: e.target.value })}
                        />
                    </div>
                    <label className="flex items-center justify-between gap-4 rounded-md border border-border bg-muted/20 px-3 py-2.5">
                        <span>
                            <span className="block text-sm font-medium">{t('accountManager.accountEnabledLabel')}</span>
                            <span className="block text-xs text-muted-foreground">{t('accountManager.accountEnabledHint')}</span>
                        </span>
                        <input
                            type="checkbox"
                            checked={newAccount.enabled !== false}
                            onChange={e => setNewAccount({ ...newAccount, enabled: e.target.checked })}
                            className="h-4 w-4 accent-emerald-600"
                        />
                    </label>
                    <div className="flex justify-end gap-2 pt-2">
                        <button onClick={onClose} className="btn btn-secondary">{t('actions.cancel')}</button>
                        <button onClick={onAdd} disabled={loading} className="btn btn-primary">
                            {loading ? t('accountManager.addAccountLoading') : t('accountManager.addAccountAction')}
                        </button>
                    </div>
                </div>
            </div>
        </div>
    )
}
