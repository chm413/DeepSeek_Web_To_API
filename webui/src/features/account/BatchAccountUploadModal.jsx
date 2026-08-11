import { useEffect, useRef, useState } from 'react'
import { FileJson, Loader2, LockKeyhole, Upload, X } from 'lucide-react'

export default function BatchAccountUploadModal({ show, t, loading, onClose, onUpload }) {
    const inputRef = useRef(null)
    const [payload, setPayload] = useState(null)
    const [fileName, setFileName] = useState('')
    const [duplicatePolicy, setDuplicatePolicy] = useState('skip')
    const [result, setResult] = useState(null)
    const [error, setError] = useState('')

    const reset = () => {
        setPayload(null)
        setFileName('')
        setDuplicatePolicy('skip')
        setResult(null)
        setError('')
        if (inputRef.current) inputRef.current.value = ''
    }

    useEffect(() => {
        if (!show) reset()
    }, [show])

    if (!show) return null

    const selectFile = async (event) => {
        const file = event.target.files?.[0]
        reset()
        if (!file) return
        try {
            const parsed = JSON.parse(await file.text())
            const nextPayload = Array.isArray(parsed) ? { accounts: parsed } : parsed
            if (!nextPayload || !Array.isArray(nextPayload.accounts) || nextPayload.accounts.length === 0) {
                throw new Error(t('accountManager.batchUploadInvalidFile'))
            }
            setPayload(nextPayload)
            setFileName(file.name)
            setDuplicatePolicy(nextPayload.on_duplicate === 'update' ? 'update' : 'skip')
        } catch (err) {
            setError(err.message || t('accountManager.batchUploadInvalidFile'))
        }
    }

    const submit = async (dryRun) => {
        if (!payload) return
        setError('')
        try {
            const data = await onUpload({ ...payload, on_duplicate: duplicatePolicy, dry_run: dryRun })
            setResult(data)
            if (!dryRun && Number(data.invalid || 0) === 0) {
                reset()
                onClose()
            }
        } catch (err) {
            setError(err.message || t('messages.requestFailed'))
        }
    }

    return (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/45 backdrop-blur-sm p-4 animate-in fade-in">
            <div className="ops-panel w-full max-w-xl overflow-hidden animate-in zoom-in-95">
                <div className="flex items-center justify-between border-b border-border p-4">
                    <div className="flex items-center gap-2">
                        <Upload className="h-4 w-4 text-primary" />
                        <h3 className="font-black">{t('accountManager.batchUploadTitle')}</h3>
                    </div>
                    <button onClick={onClose} className="text-muted-foreground hover:text-foreground" title={t('actions.cancel')}>
                        <X className="h-5 w-5" />
                    </button>
                </div>
                <div className="space-y-4 p-5">
                    <input ref={inputRef} type="file" accept="application/json,.json" onChange={selectFile} className="hidden" />
                    <button type="button" onClick={() => inputRef.current?.click()} className="flex min-h-28 w-full items-center justify-center gap-3 rounded-md border border-dashed border-border bg-muted/20 px-4 text-sm font-bold hover:border-primary/50 hover:bg-blue-50/40">
                        <FileJson className="h-6 w-6 text-primary" />
                        <span>{fileName || t('accountManager.batchUploadSelectFile')}</span>
                    </button>

                    {payload && (
                        <div className="grid grid-cols-2 gap-3 rounded-md border border-border bg-muted/20 p-3 text-sm">
                            <div>
                                <div className="text-xs font-bold text-muted-foreground">{t('accountManager.batchUploadAccounts')}</div>
                                <div className="mt-1 text-lg font-black tabular-nums">{payload.accounts.length}</div>
                            </div>
                            <div>
                                <label className="text-xs font-bold text-muted-foreground" htmlFor="batch-duplicate-policy">{t('accountManager.batchUploadDuplicatePolicy')}</label>
                                <select id="batch-duplicate-policy" value={duplicatePolicy} onChange={event => setDuplicatePolicy(event.target.value)} className="input-field mt-1 h-8 min-h-8 py-1 text-xs">
                                    <option value="skip">{t('accountManager.batchUploadSkip')}</option>
                                    <option value="update">{t('accountManager.batchUploadUpdate')}</option>
                                </select>
                            </div>
                        </div>
                    )}

                    <div className="flex items-center gap-2 text-xs text-muted-foreground" title={t('accountManager.batchUploadCredentialProtection')}>
                        <LockKeyhole className="h-3.5 w-3.5 text-emerald-600" />
                        <span>{t('accountManager.batchUploadCredentialProtection')}</span>
                    </div>

                    {error && <div className="rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700" role="alert">{error}</div>}
                    {result && (
                        <div className="grid grid-cols-4 gap-2 rounded-md border border-border p-3 text-center text-xs">
                            <div><div className="font-black text-emerald-700">{result.created || 0}</div><div className="text-muted-foreground">{t('accountManager.batchCreated')}</div></div>
                            <div><div className="font-black text-blue-700">{result.updated || 0}</div><div className="text-muted-foreground">{t('accountManager.batchUpdated')}</div></div>
                            <div><div className="font-black text-slate-700">{result.skipped || 0}</div><div className="text-muted-foreground">{t('accountManager.batchSkipped')}</div></div>
                            <div><div className="font-black text-red-700">{result.invalid || 0}</div><div className="text-muted-foreground">{t('accountManager.batchInvalid')}</div></div>
                        </div>
                    )}

                    <div className="flex justify-end gap-2 pt-1">
                        <button onClick={onClose} className="btn btn-secondary">{t('actions.cancel')}</button>
                        <button onClick={() => submit(true)} disabled={!payload || loading} className="btn btn-secondary">
                            {loading ? <Loader2 className="h-4 w-4 animate-spin" /> : null}
                            {t('accountManager.batchUploadValidate')}
                        </button>
                        <button onClick={() => submit(false)} disabled={!payload || loading} className="btn btn-primary">
                            {loading ? <Loader2 className="h-4 w-4 animate-spin" /> : <Upload className="h-4 w-4" />}
                            {t('accountManager.batchUploadImport')}
                        </button>
                    </div>
                </div>
            </div>
        </div>
    )
}
