import { useCallback, useEffect, useRef, useState } from 'react'

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
    const pageRef = useRef(1)
    const pageSizeRef = useRef(10)
    const searchQueryRef = useRef('')

    const resolveAccountIdentifier = (acc) => {
        if (!acc || typeof acc !== 'object') return ''
        return String(acc.identifier || acc.email || acc.mobile || '').trim()
    }

    const [searchQuery, setSearchQuery] = useState('')

    const fetchAccounts = useCallback(async (
        targetPage = pageRef.current,
        targetPageSize = pageSizeRef.current,
        targetQuery = searchQueryRef.current,
        { background = false } = {},
    ) => {
        const requestedPage = Math.max(1, Number(targetPage) || 1)
        const requestedPageSize = Math.max(1, Number(targetPageSize) || 10)
        const requestedQuery = String(targetQuery || '')
        const requestSequence = ++accountsRequestSequence.current
        if (!background) setLoadingAccounts(true)
        try {
            let url = `/admin/accounts?page=${requestedPage}&page_size=${requestedPageSize}`
            if (requestedQuery.trim()) url += `&q=${encodeURIComponent(requestedQuery.trim())}`
            const res = await apiFetch(url)
            if (res.ok && requestSequence === accountsRequestSequence.current) {
                const data = await res.json()
                const nextTotalPages = Math.max(1, Number(data.total_pages) || 1)
                const responsePage = Math.max(1, Number(data.page) || requestedPage)
                const nextPage = Math.min(responsePage, nextTotalPages)
                const nextPageSize = Math.max(1, Number(data.page_size) || requestedPageSize)
                setAccounts(data.items || [])
                setTotalPages(nextTotalPages)
                setTotalAccounts(data.total || 0)
                pageRef.current = nextPage
                pageSizeRef.current = nextPageSize
                setPage(nextPage)
                setPageSize(nextPageSize)
            }
        } catch (e) {
            console.error('Failed to fetch accounts:', e)
        } finally {
            if (requestSequence === accountsRequestSequence.current) {
                setLoadingAccounts(false)
            }
        }
    }, [apiFetch])

    const refreshAccounts = useCallback((options = {}) => (
        fetchAccounts(pageRef.current, pageSizeRef.current, searchQueryRef.current, {
            background: true,
            ...options,
        })
    ), [fetchAccounts])

    const changePage = useCallback((newPage) => {
        const nextPage = Math.max(1, Math.min(Number(newPage) || 1, totalPages))
        if (nextPage === pageRef.current) return
        // A page change must invalidate a late response for the previous page.
        accountsRequestSequence.current += 1
        pageRef.current = nextPage
        setPage(nextPage)
    }, [totalPages])

    const changePageSize = useCallback((newSize) => {
        const nextSize = Math.max(1, Number(newSize) || 10)
        accountsRequestSequence.current += 1
        pageRef.current = 1
        pageSizeRef.current = nextSize
        setPage(1)
        setPageSize(nextSize)
    }, [])

    const handleSearchChange = useCallback((query) => {
        const nextQuery = String(query || '')
        accountsRequestSequence.current += 1
        pageRef.current = 1
        searchQueryRef.current = nextQuery
        setPage(1)
        setSearchQuery(nextQuery)
    }, [])

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
        refreshAccounts,
        changePage,
        changePageSize,
        resolveAccountIdentifier,
        searchQuery,
        handleSearchChange,
    }
}
