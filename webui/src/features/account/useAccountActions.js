import { useState } from 'react'

const accountTestPhaseTranslationKey = {
    token_refresh: 'accountManager.testPhaseTokenRefresh',
    token_refresh_retry: 'accountManager.testPhaseTokenRefreshRetry',
    session_create: 'accountManager.testPhaseSessionCreate',
    session_create_retry: 'accountManager.testPhaseSessionCreateRetry',
    model_validation: 'accountManager.testPhaseModelValidation',
    pow: 'accountManager.testPhasePow',
    completion: 'accountManager.testPhaseCompletion',
}

function accountTestFailureMessage(accountID, data, t) {
    const phase = t(accountTestPhaseTranslationKey[data?.phase] || 'accountManager.testPhaseUnknown')
    const reason = String(data?.failure_reason || data?.message || data?.detail || t('messages.requestFailed')).trim()
    return t('accountManager.testFailureDetail', { account: accountID, phase, reason })
}

export function useAccountActions({ apiFetch, t, onMessage, onRefresh, config, totalAccounts, refreshAccounts, resolveAccountIdentifier }) {
    const [showAddKey, setShowAddKey] = useState(false)
    const [editingKey, setEditingKey] = useState(null)
    const [showAddAccount, setShowAddAccount] = useState(false)
    const [showBatchUpload, setShowBatchUpload] = useState(false)
    const [showEditAccount, setShowEditAccount] = useState(false)
    const [editingAccount, setEditingAccount] = useState(null)
    const [newKey, setNewKey] = useState({ key: '', name: '', remark: '' })
    const [copiedKey, setCopiedKey] = useState(null)
    const [newAccount, setNewAccount] = useState({ name: '', remark: '', email: '', mobile: '', password: '', enabled: true })
    const [editAccount, setEditAccount] = useState({ name: '', remark: '', enabled: true })
    const [loading, setLoading] = useState(false)
    const [testing, setTesting] = useState({})
    const [testingAll, setTestingAll] = useState(false)
    const [batchProgress, setBatchProgress] = useState({ current: 0, total: 0, results: [] })
    const [sessionCounts, setSessionCounts] = useState({})
    const [deletingSessions, setDeletingSessions] = useState({})
    const [updatingProxy, setUpdatingProxy] = useState({})
    const [updatingEnabled, setUpdatingEnabled] = useState({})
    const [batchUploading, setBatchUploading] = useState(false)
    const [batchActionLoading, setBatchActionLoading] = useState(false)

    const readJSONResponse = async (res) => {
        const text = await res.text()
        if (!text.trim()) return {}
        try {
            return JSON.parse(text)
        } catch (_err) {
            return { detail: text.trim() }
        }
    }

    const fetchAllAccountsForBatch = async () => {
        const pageSize = 5000
        let currentPage = 1
        let totalPages = 1
        const allAccounts = []

        while (currentPage <= totalPages) {
            const res = await apiFetch(`/admin/accounts?page=${currentPage}&page_size=${pageSize}`)
            const data = await readJSONResponse(res)
            if (!res.ok) {
                throw new Error(data.detail || data.message || t('messages.requestFailed'))
            }
            const items = Array.isArray(data.items) ? data.items : []
            allAccounts.push(...items)
            totalPages = Math.max(1, Number(data.total_pages) || 1)
            currentPage += 1
        }

        return allAccounts
    }

    const openAddKey = () => {
        setEditingKey(null)
        setNewKey({ key: '', name: '', remark: '' })
        setShowAddKey(true)
    }

    const openEditKey = (item) => {
        if (!item?.key) return
        setEditingKey(item)
        setNewKey({
            key: item.key || '',
            name: item.name || '',
            remark: item.remark || '',
        })
        setShowAddKey(true)
    }

    const closeKeyModal = () => {
        setShowAddKey(false)
        setEditingKey(null)
        setNewKey({ key: '', name: '', remark: '' })
    }

    const openAddAccount = () => {
        setShowEditAccount(false)
        setEditingAccount(null)
        setEditAccount({ name: '', remark: '', enabled: true })
        setNewAccount({ name: '', remark: '', email: '', mobile: '', password: '', enabled: true })
        setShowAddAccount(true)
    }

    const closeAddAccount = () => {
        setShowAddAccount(false)
        setNewAccount({ name: '', remark: '', email: '', mobile: '', password: '', enabled: true })
    }

    const openEditAccount = (account) => {
        const identifier = resolveAccountIdentifier(account)
        if (!identifier) {
            onMessage('error', t('accountManager.invalidIdentifier'))
            return
        }
        setShowAddAccount(false)
        setEditingAccount({
            identifier,
        })
        setEditAccount({
            name: account?.name || '',
            remark: account?.remark || '',
            enabled: account?.enabled !== false,
        })
        setShowEditAccount(true)
    }

    const closeEditAccount = () => {
        setShowEditAccount(false)
        setEditingAccount(null)
        setEditAccount({ name: '', remark: '', enabled: true })
    }

    const addKey = async () => {
        const isEditing = Boolean(editingKey?.key)
        if (!newKey.key.trim()) {
            return
        }
        setLoading(true)
        try {
            const endpoint = isEditing
                ? `/admin/keys/${encodeURIComponent(editingKey.key)}`
                : '/admin/keys'
            const method = isEditing ? 'PUT' : 'POST'
            const payload = isEditing
                ? { key: newKey.key.trim(), name: newKey.name, remark: newKey.remark }
                : { key: newKey.key.trim(), name: newKey.name, remark: newKey.remark }
            if (!payload.key) {
                return
            }
            const res = await apiFetch(endpoint, {
                method,
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(payload),
            })
            if (res.ok) {
                onMessage('success', isEditing ? t('accountManager.updateKeySuccess') : t('accountManager.addKeySuccess'))
                closeKeyModal()
                onRefresh()
            } else {
                const data = await res.json()
                onMessage('error', data.detail || (isEditing ? t('messages.requestFailed') : t('messages.failedToAdd')))
            }
        } catch (e) {
            onMessage('error', t('messages.networkError'))
        } finally {
            setLoading(false)
        }
    }

    const deleteKey = async (key) => {
        if (!confirm(t('accountManager.deleteKeyConfirm'))) return
        try {
            const res = await apiFetch(`/admin/keys/${encodeURIComponent(key)}`, { method: 'DELETE' })
            if (res.ok) {
                onMessage('success', t('messages.deleted'))
                onRefresh()
            } else {
                onMessage('error', t('messages.deleteFailed'))
            }
        } catch (e) {
            onMessage('error', t('messages.networkError'))
        }
    }

    const addAccount = async () => {
        if (!newAccount.password || (!newAccount.email && !newAccount.mobile)) {
            onMessage('error', t('accountManager.requiredFields'))
            return
        }
        setLoading(true)
        try {
            const res = await apiFetch('/admin/accounts', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(newAccount),
            })
            if (res.ok) {
                onMessage('success', t('accountManager.addAccountSuccess'))
                closeAddAccount()
                await refreshAccounts()
                onRefresh()
            } else {
                const data = await res.json()
                onMessage('error', data.detail || t('messages.failedToAdd'))
            }
        } catch (e) {
            onMessage('error', t('messages.networkError'))
        } finally {
            setLoading(false)
        }
    }

    const updateAccount = async () => {
        const identifier = String(editingAccount?.identifier || '').trim()
        if (!identifier) {
            onMessage('error', t('accountManager.invalidIdentifier'))
            return
        }
        setLoading(true)
        try {
            const res = await apiFetch(`/admin/accounts/${encodeURIComponent(identifier)}`, {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(editAccount),
            })
            if (res.ok) {
                onMessage('success', t('accountManager.updateAccountSuccess'))
                closeEditAccount()
                await refreshAccounts()
                onRefresh()
            } else {
                const data = await res.json()
                onMessage('error', data.detail || t('messages.requestFailed'))
            }
        } catch (e) {
            onMessage('error', t('messages.networkError'))
        } finally {
            setLoading(false)
        }
    }

    const deleteAccount = async (id) => {
        const identifier = String(id || '').trim()
        if (!identifier) {
            onMessage('error', t('accountManager.invalidIdentifier'))
            return false
        }
        if (!confirm(t('accountManager.deleteAccountConfirm'))) return false
        try {
            const res = await apiFetch(`/admin/accounts/${encodeURIComponent(identifier)}`, { method: 'DELETE' })
            if (res.ok) {
                onMessage('success', t('messages.deleted'))
                await refreshAccounts()
                onRefresh()
                return true
            } else {
                onMessage('error', t('messages.deleteFailed'))
                return false
            }
        } catch (e) {
            onMessage('error', t('messages.networkError'))
            return false
        }
    }

    const testAccount = async (identifier) => {
        const accountID = String(identifier || '').trim()
        if (!accountID) {
            onMessage('error', t('accountManager.invalidIdentifier'))
            return
        }
        setTesting(prev => ({ ...prev, [accountID]: true }))
        try {
            const res = await apiFetch('/admin/accounts/test', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ identifier: accountID }),
            })
            const data = await readJSONResponse(res)
            
            // 更新会话数
            if (data.session_count !== undefined) {
                setSessionCounts(prev => ({ ...prev, [accountID]: data.session_count }))
            }
            
            const succeeded = res.ok && Boolean(data.success)
            const statusMessage = succeeded
                ? t('apiTester.testSuccess', { account: accountID, time: data.response_time })
                : accountTestFailureMessage(accountID, data, t)
            onMessage(succeeded ? 'success' : 'error', statusMessage)
            await refreshAccounts()
            onRefresh()
        } catch (e) {
            onMessage('error', t('accountManager.testFailed', { error: e.message }))
        } finally {
            setTesting(prev => ({ ...prev, [accountID]: false }))
        }
    }

    const testAllAccounts = async () => {
        if (!confirm(t('accountManager.testAllConfirm'))) return
        const expectedTotal = totalAccounts || config?.accounts?.length || 0
        if (expectedTotal === 0) return

        setTestingAll(true)
        setBatchProgress({ current: 0, total: expectedTotal, results: [] })

        try {
            const allAccounts = await fetchAllAccountsForBatch()
            if (allAccounts.length === 0) {
                setBatchProgress({ current: 0, total: 0, results: [] })
                return
            }

            const total = allAccounts.length
            const results = []
            let completed = 0
            let successCount = 0
            let lastTableRefreshAt = 0

            setBatchProgress({ current: 0, total, results: [] })

            const refreshVisibleAccounts = () => {
                const now = Date.now()
                if (completed !== total && now - lastTableRefreshAt < 1500) return
                lastTableRefreshAt = now
                refreshAccounts()
            }

            const recordResult = (result) => {
                results.push(result)
                completed += 1
                if (result.success) successCount += 1
                if (result.sessionCount !== undefined && result.id !== '-') {
                    setSessionCounts(prev => ({ ...prev, [result.id]: result.sessionCount }))
                }
                setBatchProgress({ current: completed, total, results: [...results] })
                refreshVisibleAccounts()
            }

            const testOne = async (acc) => {
                const id = resolveAccountIdentifier(acc)
                if (!id) {
                    return { id: '-', success: false, message: t('accountManager.invalidIdentifier') }
                }
                try {
                    const res = await apiFetch('/admin/accounts/test', {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({ identifier: id, model: 'deepseek-v4-flash' }),
                    })
                    const data = await readJSONResponse(res)
                    return {
                        id,
                        success: res.ok && Boolean(data.success),
                        message: data.success
                            ? (data.message || '')
                            : accountTestFailureMessage(id, data, t),
                        time: data.response_time,
                        sessionCount: data.session_count,
                    }
                } catch (e) {
                    return { id, success: false, message: e.message }
                }
            }

            let nextIndex = 0
            const workerCount = Math.min(5, total)
            const workers = Array.from({ length: workerCount }, async () => {
                while (nextIndex < total) {
                    const account = allAccounts[nextIndex]
                    nextIndex += 1
                    recordResult(await testOne(account))
                }
            })

            await Promise.all(workers)

            await refreshAccounts()
            await onRefresh()
            onMessage('success', t('accountManager.testAllCompleted', { success: successCount, total }))
        } catch (e) {
            onMessage('error', t('accountManager.testFailed', { error: e.message }))
        } finally {
            setTestingAll(false)
        }
    }

    const deleteAllSessions = async (identifier) => {
        const accountID = String(identifier || '').trim()
        if (!accountID) {
            onMessage('error', t('accountManager.invalidIdentifier'))
            return
        }
        if (!confirm(t('accountManager.deleteAllSessionsConfirm'))) return
        
        setDeletingSessions(prev => ({ ...prev, [accountID]: true }))
        try {
            const res = await apiFetch('/admin/accounts/sessions/delete-all', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ identifier: accountID }),
            })
            const data = await res.json()
            
            if (data.success) {
                onMessage('success', t('accountManager.deleteAllSessionsSuccess'))
                setSessionCounts(prev => ({ ...prev, [accountID]: 0 }))
                await refreshAccounts()
            } else {
                onMessage('error', data.message || t('messages.requestFailed'))
            }
        } catch (e) {
            onMessage('error', t('messages.networkError'))
        } finally {
            setDeletingSessions(prev => ({ ...prev, [accountID]: false }))
        }
    }

    const updateAccountProxy = async (identifier, proxyID, autoRoute = false) => {
        const accountID = String(identifier || '').trim()
        if (!accountID) {
            onMessage('error', t('accountManager.invalidIdentifier'))
            return
        }
        setUpdatingProxy(prev => ({ ...prev, [accountID]: true }))
        try {
            const res = await apiFetch(`/admin/accounts/${encodeURIComponent(accountID)}/proxy`, {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ proxy_id: proxyID || '', auto_route: Boolean(autoRoute) }),
            })
            const data = await res.json()
            if (!res.ok) {
                onMessage('error', data.detail || t('messages.requestFailed'))
                return
            }
            if (data.relogin?.success === false) {
                onMessage('error', t('accountManager.proxyReloginFailed', { reason: data.relogin.reason || t('messages.requestFailed') }))
            } else {
                onMessage('success', autoRoute ? t('accountManager.proxyAutoEnabled') : t('accountManager.proxyUpdateSuccess'))
            }
            await refreshAccounts()
            onRefresh()
        } catch (_err) {
            onMessage('error', t('messages.networkError'))
        } finally {
            setUpdatingProxy(prev => ({ ...prev, [accountID]: false }))
        }
    }

    const openBatchUpload = () => setShowBatchUpload(true)
    const closeBatchUpload = () => setShowBatchUpload(false)

    const uploadBatchAccounts = async (payload) => {
        setBatchUploading(true)
        try {
            const res = await apiFetch('/admin/accounts/batch', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(payload),
            })
            const data = await readJSONResponse(res)
            if (!res.ok) {
                throw new Error(data.detail || t('messages.requestFailed'))
            }
            if (!payload.dry_run) {
                await refreshAccounts()
                await onRefresh()
                onMessage(data.invalid > 0 ? 'error' : 'success', t('accountManager.batchUploadResult', {
                    created: data.created || 0,
                    updated: data.updated || 0,
                    skipped: data.skipped || 0,
                    invalid: data.invalid || 0,
                }))
            }
            return data
        } finally {
            setBatchUploading(false)
        }
    }

    const updateAccountEnabled = async (identifier, enabled) => {
        const accountID = String(identifier || '').trim()
        if (!accountID) {
            onMessage('error', t('accountManager.invalidIdentifier'))
            return
        }
        setUpdatingEnabled(prev => ({ ...prev, [accountID]: true }))
        try {
            const res = await apiFetch(`/admin/accounts/${encodeURIComponent(accountID)}`, {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ enabled }),
            })
            const data = await readJSONResponse(res)
            if (!res.ok) {
                onMessage('error', data.detail || t('messages.requestFailed'))
                return
            }
            onMessage('success', enabled ? t('accountManager.accountEnabled') : t('accountManager.accountDisabled'))
            await refreshAccounts()
            onRefresh()
        } catch (_err) {
            onMessage('error', t('messages.networkError'))
        } finally {
            setUpdatingEnabled(prev => ({ ...prev, [accountID]: false }))
        }
    }

    const applyBatchAccountAction = async (identifiers, action, { proxyID = '', autoRoute = false } = {}) => {
        const accountIDs = [...new Set((identifiers || []).map(value => String(value || '').trim()).filter(Boolean))]
        if (accountIDs.length === 0) {
            onMessage('error', t('accountManager.batchSelectionRequired'))
            return null
        }
        setBatchActionLoading(true)
        try {
            const payload = { identifiers: accountIDs, action }
            if (action === 'set_proxy') {
                payload.proxy_id = proxyID || ''
                payload.auto_route = Boolean(autoRoute)
            }
            const res = await apiFetch('/admin/accounts/batch/actions', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(payload),
            })
            const data = await readJSONResponse(res)
            if (!res.ok) {
                onMessage('error', data.detail || t('messages.requestFailed'))
                return null
            }
            await refreshAccounts()
            await onRefresh()
            const affected = Number(data.affected) || accountIDs.length
            const reloginFailed = Number(data?.relogin?.failed) || 0
            if (reloginFailed > 0) {
                onMessage('error', t('accountManager.batchActionReloginFailed', { affected, failed: reloginFailed }))
            } else if (action === 'set_proxy') {
                onMessage('success', t('accountManager.batchProxyUpdated', { count: affected }))
            } else if (action === 'enable') {
                onMessage('success', t('accountManager.batchAccountsEnabled', { count: affected }))
            } else {
                onMessage('success', t('accountManager.batchAccountsDisabled', { count: affected }))
            }
            return data
        } catch (_err) {
            onMessage('error', t('messages.networkError'))
            return null
        } finally {
            setBatchActionLoading(false)
        }
    }

    return {
        showAddKey,
        openAddKey,
        openEditKey,
        closeKeyModal,
        editingKey,
        showAddAccount,
        openAddAccount,
        closeAddAccount,
        showBatchUpload,
        openBatchUpload,
        closeBatchUpload,
        batchUploading,
        batchActionLoading,
        uploadBatchAccounts,
        showEditAccount,
        editingAccount,
        editAccount,
        setEditAccount,
        openEditAccount,
        closeEditAccount,
        newKey,
        setNewKey,
        copiedKey,
        setCopiedKey,
        newAccount,
        setNewAccount,
        loading,
        testing,
        testingAll,
        batchProgress,
        sessionCounts,
        deletingSessions,
        updatingProxy,
        updatingEnabled,
        addKey,
        deleteKey,
        addAccount,
        updateAccount,
        deleteAccount,
        testAccount,
        testAllAccounts,
        deleteAllSessions,
        updateAccountProxy,
        updateAccountEnabled,
        applyBatchAccountAction,
    }
}
