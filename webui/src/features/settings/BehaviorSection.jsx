export default function BehaviorSection({ t, form, setForm }) {
    return (
        <div className="bg-card border border-border rounded-xl p-5 space-y-4">
            <h3 className="font-semibold">{t('settings.behaviorTitle')}</h3>
            <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                <label className="text-sm space-y-2">
                    <span className="text-muted-foreground">{t('settings.responsesTTL')}</span>
                    <input
                        type="number"
                        min={30}
                        value={form.responses.store_ttl_seconds}
                        onChange={(e) => setForm((prev) => ({
                            ...prev,
                            responses: { ...prev.responses, store_ttl_seconds: Number(e.target.value || 30) },
                        }))}
                        className="w-full bg-background border border-border rounded-lg px-3 py-2"
                    />
                </label>
                <label className="text-sm space-y-2">
                    <span className="text-muted-foreground">{t('settings.embeddingsProvider')}</span>
                    <input
                        type="text"
                        value={form.embeddings.provider}
                        onChange={(e) => setForm((prev) => ({
                            ...prev,
                            embeddings: { ...prev.embeddings, provider: e.target.value },
                        }))}
                        className="w-full bg-background border border-border rounded-lg px-3 py-2"
                    />
                </label>
                <label className="flex items-start gap-3 rounded-lg border border-border bg-background/60 p-4">
                    <input
                        type="checkbox"
                        checked={Boolean(form.thinking_injection?.enabled ?? true)}
                        onChange={(e) => setForm((prev) => ({
                            ...prev,
                            thinking_injection: {
                                ...prev.thinking_injection,
                                enabled: e.target.checked,
                            },
                        }))}
                        className="mt-1 h-4 w-4 rounded border-border"
                    />
                    <div className="space-y-1">
                        <span className="text-sm font-medium block">{t('settings.thinkingInjectionEnabled')}</span>
                        <span className="text-xs text-muted-foreground block">{t('settings.thinkingInjectionDesc')}</span>
                    </div>
                </label>
                <label className="flex items-start gap-3 rounded-lg border border-border bg-background/60 p-4">
                    <input
                        type="checkbox"
                        checked={Boolean(form.prompt_limit?.enabled ?? true)}
                        onChange={(e) => setForm((prev) => ({
                            ...prev,
                            prompt_limit: {
                                ...prev.prompt_limit,
                                enabled: e.target.checked,
                            },
                        }))}
                        className="mt-1 h-4 w-4 rounded border-border"
                    />
                    <span className="text-sm font-medium block">{t('settings.promptLimitEnabled')}</span>
                </label>
                <label className="flex items-start gap-3 rounded-lg border border-border bg-background/60 p-4">
                    <input
                        type="checkbox"
                        checked={Boolean(form.prompt_limit?.auto_compress_enabled)}
                        disabled={!form.prompt_limit?.enabled}
                        onChange={(e) => setForm((prev) => ({
                            ...prev,
                            prompt_limit: {
                                ...prev.prompt_limit,
                                auto_compress_enabled: e.target.checked,
                            },
                        }))}
                        className="mt-1 h-4 w-4 rounded border-border disabled:opacity-50"
                    />
                    <span className="text-sm font-medium block">{t('settings.autoCompressEnabled')}</span>
                </label>
                <label className="text-sm space-y-2">
                    <span className="text-muted-foreground">{t('settings.compressKeepRecent')}</span>
                    <input
                        type="number"
                        min={1}
                        max={100}
                        value={form.prompt_limit?.compress_keep_recent || 6}
                        disabled={!form.prompt_limit?.enabled || !form.prompt_limit?.auto_compress_enabled}
                        onChange={(e) => setForm((prev) => ({
                            ...prev,
                            prompt_limit: {
                                ...prev.prompt_limit,
                                compress_keep_recent: Number(e.target.value || 6),
                            },
                        }))}
                        className="w-full bg-background border border-border rounded-lg px-3 py-2 disabled:opacity-50"
                    />
                </label>
                <label className="flex items-start gap-3 rounded-lg border border-border bg-background/60 p-4">
                    <input
                        type="checkbox"
                        checked={Boolean(form.prompt_limit?.compress_keep_system ?? true)}
                        disabled={!form.prompt_limit?.enabled || !form.prompt_limit?.auto_compress_enabled}
                        onChange={(e) => setForm((prev) => ({
                            ...prev,
                            prompt_limit: {
                                ...prev.prompt_limit,
                                compress_keep_system: e.target.checked,
                            },
                        }))}
                        className="mt-1 h-4 w-4 rounded border-border disabled:opacity-50"
                    />
                    <span className="text-sm font-medium block">{t('settings.compressKeepSystem')}</span>
                </label>
                <label className="flex items-start gap-3 rounded-lg border border-border bg-background/60 p-4">
                    <input
                        type="checkbox"
                        checked={Boolean(form.prompt_limit?.pro_flash_compression_enabled)}
                        onChange={(e) => setForm((prev) => ({
                            ...prev,
                            prompt_limit: {
                                ...prev.prompt_limit,
                                pro_flash_compression_enabled: e.target.checked,
                            },
                        }))}
                        className="mt-1 h-4 w-4 rounded border-border"
                    />
                    <div className="space-y-1">
                        <span className="text-sm font-medium block">{t('settings.proFlashCompressionEnabled')}</span>
                        <span className="text-xs text-muted-foreground block">{t('settings.proFlashCompressionDesc')}</span>
                    </div>
                </label>
                <label className="text-sm space-y-2">
                    <span className="text-muted-foreground">{t('settings.proFlashCompressionTarget')}</span>
                    <input
                        type="number"
                        min={1}
                        max={1000000}
                        value={form.prompt_limit?.pro_flash_compression_target_chars || 150000}
                        onChange={(e) => setForm((prev) => ({
                            ...prev,
                            prompt_limit: {
                                ...prev.prompt_limit,
                                pro_flash_compression_target_chars: Number(e.target.value || 150000),
                            },
                        }))}
                        className="w-full bg-background border border-border rounded-lg px-3 py-2"
                    />
                </label>
                <label className="text-sm space-y-2">
                    <span className="text-muted-foreground">{t('settings.incrementalMaxTurns')}</span>
                    <input
                        type="number"
                        min={0}
                        max={1000000}
                        value={form.prompt_limit?.incremental_max_turns ?? 0}
                        onChange={(e) => setForm((prev) => ({
                            ...prev,
                            prompt_limit: {
                                ...prev.prompt_limit,
                                incremental_max_turns: Number(e.target.value || 0),
                            },
                        }))}
                        className="w-full bg-background border border-border rounded-lg px-3 py-2"
                    />
                </label>
                <label className="text-sm space-y-2">
                    <span className="text-muted-foreground">{t('settings.incrementalRotationKeepRecent')}</span>
                    <input
                        type="number"
                        min={1}
                        max={100}
                        value={form.prompt_limit?.incremental_rotation_keep_recent || 6}
                        onChange={(e) => setForm((prev) => ({
                            ...prev,
                            prompt_limit: {
                                ...prev.prompt_limit,
                                incremental_rotation_keep_recent: Number(e.target.value || 6),
                            },
                        }))}
                        className="w-full bg-background border border-border rounded-lg px-3 py-2"
                    />
                </label>
                <label className="text-sm space-y-2 md:col-span-2">
                    <span className="text-muted-foreground">{t('settings.thinkingInjectionPrompt')}</span>
                    <textarea
                        rows={5}
                        value={form.thinking_injection?.prompt || ''}
                        placeholder={form.thinking_injection?.default_prompt || ''}
                        onChange={(e) => setForm((prev) => ({
                            ...prev,
                            thinking_injection: {
                                ...prev.thinking_injection,
                                prompt: e.target.value,
                            },
                        }))}
                        className="w-full bg-background border border-border rounded-lg px-3 py-2 resize-y min-h-32"
                    />
                    <p className="text-xs text-muted-foreground">{t('settings.thinkingInjectionPromptHelp')}</p>
                </label>
            </div>
        </div>
    )
}
