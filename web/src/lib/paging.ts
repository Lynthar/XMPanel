import { useEffect, useState } from 'react'
import type { ListParams } from '@/lib/api'

/** One query's paging state: a search term and a stack of cursors visited. */
export interface Paging {
  search: string
  setSearch: (value: string) => void
  /** Query parameters for the current page, ready for the API client. */
  params: ListParams
  hasPrev: boolean
  next: (cursor: string) => void
  prev: () => void
  reset: () => void
}

/**
 * usePaging keeps the cursor stack for a cursor-paged listing. A search
 * change returns to the first page; the term reaches `params` after a short
 * pause so typing does not fire a request per keystroke.
 *
 * @param limit - page size sent to the server
 * @param debounceMs - delay before a typed search term is applied
 */
export function usePaging(limit = 50, debounceMs = 300): Paging {
  const [search, setSearch] = useState('')
  const [applied, setApplied] = useState('')
  const [cursors, setCursors] = useState<string[]>([''])

  useEffect(() => {
    const handle = setTimeout(() => {
      setApplied(search)
      setCursors([''])
    }, debounceMs)
    return () => clearTimeout(handle)
  }, [search, debounceMs])

  const cursor = cursors[cursors.length - 1]
  return {
    search,
    setSearch,
    params: { limit, search: applied || undefined, cursor: cursor || undefined },
    hasPrev: cursors.length > 1,
    next: (c) => setCursors((stack) => [...stack, c]),
    prev: () => setCursors((stack) => (stack.length > 1 ? stack.slice(0, -1) : stack)),
    reset: () => setCursors(['']),
  }
}
