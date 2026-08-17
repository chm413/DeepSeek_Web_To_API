import { AlertTriangle, Save } from 'lucide-react'

import { useI18n } from '../../i18n'
import { useSettingsForm } from './useSettingsForm'
import SecuritySection from './SecuritySection'
import SafetyPolicySection from './SafetyPolicySection'
import RuntimeSection from './RuntimeSection'
import BehaviorSection from './BehaviorSection'
import CacheSection from './CacheSection'
import CurrentInputFileSection from './HistorySplitSection'
import CompatibilitySection from './CompatibilitySection'
import AutoDeleteSection from './AutoDeleteSection'
import ModelSection from './ModelSection'
import BackupSection from './BackupSection'
import UpdateSection from './UpdateSection'

export default function SettingsContainer({ onRefresh, onMessage, authFetch, onForceLogout, onUpdateStatus }) {
    const { t } = useI18n()
    const apiFetch = authFetch || fetch

    const {
        form,
        setForm,
        loading,
        saving,
        changingPassword,
        importing,
        exportData,
        importMode,
        setImportMode,
        importText,
        setImportText,
        newPassword,
        setNewPassword,
        settingsMeta,
        saveSettings,
        updatePassword,
        loadExportData,
        downloadExportFile,
        loadImportFile,
        doImport,
    } = useSettingsForm({
        apiFetch,
        t,
        onMessage,
        onRefresh,
        onForceLogout,
    })

    return (
        <div className="space-y-6">
            {settingsMeta.default_password_warning && (
                <div className="p-4 rounded-lg border border-amber-300/30 bg-amber-500/10 text-amber-700 flex items-center gap-2">
                    <AlertTriangle className="w-4 h-4" />
                    <span className="text-sm">{t('settings.defaultPasswordWarning')}</span>
                </div>
            )}

            <SecuritySection
                t={t}
                form={form}
                setForm={setForm}
                newPassword={newPassword}
                setNewPassword={setNewPassword}
                changingPassword={changingPassword}
                onUpdatePassword={updatePassword}
            />

            <SafetyPolicySection t={t} form={form} setForm={setForm} />

            <RuntimeSection t={t} form={form} setForm={setForm} />

            <BehaviorSection t={t} form={form} setForm={setForm} />

            <CacheSection t={t} form={form} setForm={setForm} />

            <CurrentInputFileSection t={t} form={form} setForm={setForm} />

            <CompatibilitySection t={t} form={form} setForm={setForm} />

            <AutoDeleteSection t={t} form={form} setForm={setForm} />

            <ModelSection t={t} form={form} setForm={setForm} />

            <UpdateSection t={t} authFetch={apiFetch} onMessage={onMessage} onStatusChange={onUpdateStatus} />

            <BackupSection
                t={t}
                importMode={importMode}
                setImportMode={setImportMode}
                importing={importing}
                onLoadExportData={loadExportData}
                onDownloadExportFile={downloadExportFile}
                onImport={doImport}
                onImportFileChange={loadImportFile}
                importText={importText}
                setImportText={setImportText}
                exportData={exportData}
            />

            <div className="flex justify-end">
                <button
                    type="button"
                    onClick={saveSettings}
                    disabled={loading || saving}
                    className="px-4 py-2 rounded-lg bg-primary text-primary-foreground hover:bg-primary/90 flex items-center gap-2"
                >
                    <Save className="w-4 h-4" />
                    {saving ? t('settings.saving') : t('settings.save')}
                </button>
            </div>
        </div>
    )
}
