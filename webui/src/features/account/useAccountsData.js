import { useEffect, useRef, useState } from 'react'

export function useAccountsData({ apiFetch }) {
    const [queueStatus, setQueueStatus] = useState(null)
    const [metrics, setMetrics] = useState(null)
    const [keysExpanded, setKeysExpanded] = useState(false)

    const [accounts, setAccounts] = useState([])
    const [page, setPage] = useState(1)
    const [pageSize, setPageSize] = useState(10)
    const [totalPages, setTotalPages] = useState(1)
    const [totalAccounts, setTotalAccounts] = useState(0)
    const [loadingAccounts, setLoadingAccounts] = useState(false)
    const accountsRequestSequence = useRef(0)

    const resolveAccountIdentifier = (acc) => {
        if (!acc || typeof acc !== 'object') return ''
        return String(acc.identifier || acc.email || acc.mobile || '').trim()
    }

    const [searchQuery, setSearchQuery] = useState('')

    const fetchAccounts = async (
        targetPage = page,
        targetPageSize = pageSize,
        targetQuery = searchQuery,
        { background = false } = {},
    ) => {
        const requestSequence = ++accountsRequestSequence.current
        if (!background) setLoadingAccounts(true)
        try {
            let url = `/admin/accounts?page=${targetPage}&page_size=${targetPageSize}`
            if (targetQuery.trim()) url += `&q=${encodeURIComponent(targetQuery.trim())}`
            const res = await apiFetch(url)
            if (res.ok && requestSequence === accountsRequestSequence.current) {
                const data = await res.json()
                setAccounts(data.items || [])
                setTotalPages(data.total_pages || 1)
                setTotalAccounts(data.total || 0)
                setPage(data.page || 1)
            }
        } catch (e) {
            console.error('Failed to fetch accounts:', e)
        } finally {
            if (requestSequence === accountsRequestSequence.current) {
                setLoadingAccounts(false)
            }
        }
    }

    const changePage = (newPage) => {
        setPage(Math.max(1, newPage))
    }

    const changePageSize = (newSize) => {
        setPage(1)
        setPageSize(newSize)
    }

    const handleSearchChange = (query) => {
        setPage(1)
        setSearchQuery(query)
    }

    const fetchQueueStatus = async () => {
        try {
            const res = await apiFetch('/admin/queue/status')
            if (res.ok) {
                const data = await res.json()
                setQueueStatus(data)
            }
        } catch (e) {
            console.error('Failed to fetch queue status:', e)
        }
    }

    const fetchMetrics = async () => {
        try {
            const res = await apiFetch('/admin/metrics/overview')
            if (res.ok) {
                setMetrics(await res.json())
            }
        } catch (e) {
            console.error('Failed to fetch account metrics:', e)
        }
    }

    useEffect(() => {
        fetchAccounts(page, pageSize, searchQuery)
        fetchQueueStatus()
        fetchMetrics()
        const interval = setInterval(() => {
            fetchAccounts(page, pageSize, searchQuery, { background: true })
            fetchQueueStatus()
            fetchMetrics()
        }, 5000)
        return () => clearInterval(interval)
    }, [page, pageSize, searchQuery])

    return {
        queueStatus,
        metrics,
        keysExpanded,
        setKeysExpanded,
        accounts,
        page,
        pageSize,
        totalPages,
        totalAccounts,
        loadingAccounts,
        fetchAccounts,
        changePage,
        changePageSize,
        resolveAccountIdentifier,
        searchQuery,
        handleSearchChange,
    }
}
