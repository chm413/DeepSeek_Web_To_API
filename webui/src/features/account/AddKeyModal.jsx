import { X } from 'lucide-react'
import { v4 as uuidv4 } from 'uuid'

export default function AddKeyModal({ show, t, editingKey, newKey, setNewKey, loading, onClose, onAdd }) {
    if (!show) {
        return null
    }

    const isEditing = Boolean(editingKey?.key || editingKey?.id)
    const displayKey = newKey.key

    return (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/45 backdrop-blur-sm p-4 animate-in fade-in">
            <div className="ops-panel w-full max-w-md overflow-hidden animate-in zoom-in-95">
                <div className="p-4 border-b border-border flex justify-between items-center">
                    <h3 className="font-black">{isEditing ? t('accountManager.modalEditKeyTitle') : t('accountManager.modalAddKeyTitle')}</h3>
                    <button onClick={onClose} className="text-muted-foreground hover:text-foreground">
                        <X className="w-5 h-5" />
                    </button>
                </div>
                <div className="p-6 space-y-4">
                    <div>
                        <label className="block text-sm font-medium mb-1.5">{isEditing ? t('accountManager.keyLabel') : t('accountManager.newKeyLabel')}</label>
                        <div className="flex gap-2">
                            <input
                                type="text"
                                className="input-field bg-card flex-1"
                                placeholder={t('accountManager.newKeyPlaceholder')}
                                value={displayKey}
                                onChange={e => setNewKey({ ...newKey, key: e.target.value })}
                                autoFocus
                            />
                            <button
                                type="button"
                                onClick={() => setNewKey({ ...newKey, key: 'sk-' + uuidv4().replace(/-/g, '') })}
                                className="btn btn-secondary whitespace-nowrap"
                            >
                                {t('accountManager.generate')}
                            </button>
                        </div>
                        <p className="text-xs text-muted-foreground mt-1.5">
                            {isEditing ? t('accountManager.keyEditableHint') : t('accountManager.generateHint')}
                        </p>
                    </div>
                    <div>
                        <label className="block text-sm font-medium mb-1.5">{t('accountManager.nameOptional')}</label>
                        <input
                            type="text"
                            className="input-field"
                            placeholder={t('accountManager.namePlaceholder')}
                            value={newKey.name}
                            onChange={e => setNewKey({ ...newKey, name: e.target.value })}
                            autoFocus={isEditing}
                        />
                    </div>
                    <div>
                        <label className="block text-sm font-medium mb-1.5">{t('accountManager.remarkOptional')}</label>
                        <input
                            type="text"
                            className="input-field"
                            placeholder={t('accountManager.remarkPlaceholder')}
                            value={newKey.remark}
                            onChange={e => setNewKey({ ...newKey, remark: e.target.value })}
                        />
                    </div>
                    <div className="flex justify-end gap-2 pt-2">
                        <button onClick={onClose} className="btn btn-secondary">{t('actions.cancel')}</button>
                        <button onClick={onAdd} disabled={loading} className="btn btn-primary">
                            {loading
                                ? (isEditing ? t('accountManager.editKeyLoading') : t('accountManager.addKeyLoading'))
                                : (isEditing ? t('accountManager.editKeyAction') : t('accountManager.addKeyAction'))}
                        </button>
                    </div>
                </div>
            </div>
        </div>
    )
}
