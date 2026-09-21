import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import toast from 'react-hot-toast'
import { LogOut } from 'lucide-react'
import { backendApi, errorMessage, listErrorMessage, type Page, type Server, type ServerCapabilities, type Session } from '@/lib/api'
import PagedTable from '@/components/PagedTable'
import { usePaging } from '@/lib/paging'
import { sessionSince, sessionState } from '@/lib/sessions'

/** Sessions tab: the server-wide session listing, paged. */
export default function Sessions({ server, caps }: { server: Server; caps: ServerCapabilities }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const words = (key: string) => t(`backend.sessions.${server.protocol}.${key}`)
  const paging = usePaging()
  const query = useQuery({
    queryKey: ['sessions', server.id, paging.params],
    queryFn: async () => (await backendApi.listSessions(server.id, paging.params)).data as Page<Session>,
    placeholderData: (previous) => previous,
    refetchInterval: 10000,
  })
  const canTerminate = caps.capabilities.includes('sessions.terminate')

  const terminate = useMutation({
    mutationFn: (s: Session) => backendApi.terminateSession(server.id, s.id, s.account_id),
    onSuccess: () => {
      toast.success(words('terminated'))
      queryClient.invalidateQueries({ queryKey: ['sessions', server.id] })
      queryClient.invalidateQueries({ queryKey: ['server-stats', server.id] })
    },
    onError: (error) => toast.error(errorMessage(error) ?? t('backend.sessions.terminateFailed')),
  })

  return (
    <PagedTable
      columns={[
        { key: 'id', header: words('id'), render: (s: Session) => <span className="font-mono text-xs text-gray-200">{s.id}</span> },
        { key: 'account', header: t('backend.accounts.id'), render: (s: Session) => <span className="font-mono text-xs text-gray-300">{s.account_id}</span> },
        { key: 'ip', header: t('backend.sessions.ip'), render: (s: Session) => <span className="font-mono text-xs text-gray-300">{s.ip || '—'}</span> },
        { key: 'since', header: words('since'), render: (s: Session) => <span className="text-gray-400">{sessionSince(s)}</span> },
        { key: 'state', header: t('common.status'), render: (s: Session) => <span className="text-gray-300">{sessionState(s, t, words)}</span> },
        ...(canTerminate
          ? [{
              key: 'actions',
              header: t('common.actions'),
              className: 'text-right w-20',
              render: (s: Session) => (
                <button onClick={() => terminate.mutate(s)} className="p-1 text-yellow-400 hover:text-yellow-300" title={words('terminate')} aria-label={words('terminate')}>
                  <LogOut className="w-4 h-4" />
                </button>
              ),
            }]
          : []),
      ]}
      page={query.data}
      loading={query.isFetching}
      error={query.isError ? listErrorMessage(query.error, t) : undefined}
      rowKey={(s) => s.id}
      paging={paging}
      searchPlaceholder={words('searchPlaceholder')}
      emptyMessage={words('empty')}
    />
  )
}
