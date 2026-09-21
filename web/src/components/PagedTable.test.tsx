import { render, screen, fireEvent, act } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import PagedTable from './PagedTable'
import { usePaging } from '@/lib/paging'
import type { Page } from '@/lib/api'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string, opts?: { count?: number }) => (opts?.count !== undefined ? `${key}:${opts.count}` : key) }),
}))

interface Row {
  id: string
}

// pages simulates a two-page cursor listing the way the backend answers it.
const pages: Record<string, Page<Row>> = {
  '': { items: [{ id: 'a' }, { id: 'b' }], next: '2', total: 3 },
  '2': { items: [{ id: 'c' }], total: 3 },
}

function Harness({ onParams }: { onParams?: (params: unknown) => void }) {
  const paging = usePaging(2, 0)
  onParams?.(paging.params)
  const page = pages[paging.params.cursor ?? ''] ?? { items: [] }
  return (
    <PagedTable<Row>
      columns={[{ key: 'id', header: 'ID', render: (r) => r.id }]}
      page={page}
      loading={false}
      rowKey={(r) => r.id}
      paging={paging}
      searchPlaceholder="search"
      emptyMessage="empty"
    />
  )
}

describe('PagedTable', () => {
  it('walks forward and back through cursors', () => {
    render(<Harness />)
    expect(screen.getByText('a')).toBeInTheDocument()
    expect(screen.getByText('paging.total:3')).toBeInTheDocument()

    const next = screen.getByRole('button', { name: /common.next/ })
    const prev = screen.getByRole('button', { name: /common.previous/ })
    expect(prev).toBeDisabled()
    fireEvent.click(next)
    expect(screen.getByText('c')).toBeInTheDocument()
    expect(screen.queryByText('a')).not.toBeInTheDocument()
    expect(next).toBeDisabled()

    fireEvent.click(prev)
    expect(screen.getByText('a')).toBeInTheDocument()
    expect(prev).toBeDisabled()
  })

  it('applies a search term after the debounce and returns to the first page', async () => {
    vi.useFakeTimers()
    const seen: unknown[] = []
    render(<Harness onParams={(p) => seen.push(p)} />)
    fireEvent.click(screen.getByRole('button', { name: /common.next/ }))
    expect(seen[seen.length - 1]).toMatchObject({ cursor: '2' })

    fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'ali' } })
    await act(async () => {
      vi.runAllTimers()
    })
    expect(seen[seen.length - 1]).toMatchObject({ search: 'ali', limit: 2 })
    expect((seen[seen.length - 1] as { cursor?: string }).cursor).toBeUndefined()
    vi.useRealTimers()
  })

  it('shows the error instead of an empty list', () => {
    const paging = { search: '', setSearch: () => {}, params: {}, hasPrev: false, next: () => {}, prev: () => {}, reset: () => {} }
    render(
      <PagedTable<Row>
        columns={[]}
        loading={false}
        error="forbidden"
        rowKey={(r) => r.id}
        paging={paging}
        searchPlaceholder="search"
        emptyMessage="empty"
      />
    )
    expect(screen.getByRole('alert')).toHaveTextContent('forbidden')
    expect(screen.queryByText('empty')).not.toBeInTheDocument()
  })
})
