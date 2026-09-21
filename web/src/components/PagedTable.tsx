import { Fragment, ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronLeft, ChevronRight, Search } from 'lucide-react'
import type { Page } from '@/lib/api'
import type { Paging } from '@/lib/paging'

export interface Column<T> {
  key: string
  header: ReactNode
  render: (row: T) => ReactNode
  className?: string
}

export interface PagedTableProps<T> {
  columns: Column<T>[]
  page?: Page<T>
  loading: boolean
  error?: string
  rowKey: (row: T) => string
  paging: Paging
  searchPlaceholder: string
  emptyMessage: string
  /** Rendered to the right of the search box. */
  toolbar?: ReactNode
  /** Optional detail row shown under a row; return null to show nothing. */
  expansion?: (row: T) => ReactNode | null
  expandedKeys?: Set<string>
}

/**
 * PagedTable renders one page of a cursor-paged listing with a search box,
 * previous/next controls and an optional per-row expansion.
 */
export default function PagedTable<T>({
  columns, page, loading, error, rowKey, paging, searchPlaceholder, emptyMessage, toolbar, expansion, expandedKeys,
}: PagedTableProps<T>) {
  const { t } = useTranslation()
  const items = page?.items ?? []
  const total = page?.total

  return (
    <div>
      <div className="flex items-center justify-between gap-4 mb-4 flex-wrap">
        <div className="relative">
          <Search className="w-4 h-4 text-gray-500 absolute left-3 top-1/2 -translate-y-1/2" />
          <input
            type="search"
            className="input pl-9 w-64"
            placeholder={searchPlaceholder}
            value={paging.search}
            onChange={(e) => paging.setSearch(e.target.value)}
            aria-label={searchPlaceholder}
          />
        </div>
        <div className="flex items-center gap-2">{toolbar}</div>
      </div>

      {error ? (
        <p className="text-center text-red-400 py-8" role="alert">{error}</p>
      ) : loading && !page ? (
        <div className="flex justify-center py-8">
          <div className="animate-spin rounded-full h-6 w-6 border-b-2 border-primary-500" />
        </div>
      ) : items.length === 0 ? (
        <p className="text-center text-gray-400 py-8">{emptyMessage}</p>
      ) : (
        <table className="w-full">
          <thead>
            <tr className="border-b border-gray-700">
              {columns.map((c) => (
                <th key={c.key} className={`table-header ${c.className ?? ''}`}>{c.header}</th>
              ))}
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-700">
            {items.map((row) => {
              const key = rowKey(row)
              const detail = expansion && expandedKeys?.has(key) ? expansion(row) : null
              return (
                <Fragment key={key}>
                  <tr className="hover:bg-gray-700/50">
                    {columns.map((c) => (
                      <td key={c.key} className={`table-cell ${c.className ?? ''}`}>{c.render(row)}</td>
                    ))}
                  </tr>
                  {detail !== null && detail !== undefined && (
                    <tr className="bg-gray-800/40">
                      <td colSpan={columns.length} className="px-4 py-3">{detail}</td>
                    </tr>
                  )}
                </Fragment>
              )
            })}
          </tbody>
        </table>
      )}

      {(paging.hasPrev || page?.next || total !== undefined) && (
        <div className="flex items-center justify-between mt-4 text-sm text-gray-400">
          <span>
            {total !== undefined
              ? t('paging.total', { count: total })
              : t('paging.shown', { count: items.length })}
          </span>
          <div className="flex items-center gap-2">
            <button
              type="button"
              className="btn btn-secondary flex items-center gap-1"
              onClick={paging.prev}
              disabled={!paging.hasPrev || loading}
            >
              <ChevronLeft className="w-4 h-4" />
              {t('common.previous')}
            </button>
            <button
              type="button"
              className="btn btn-secondary flex items-center gap-1"
              onClick={() => page?.next && paging.next(page.next)}
              disabled={!page?.next || loading}
            >
              {t('common.next')}
              <ChevronRight className="w-4 h-4" />
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
