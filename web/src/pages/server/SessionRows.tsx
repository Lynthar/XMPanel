import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import toast from 'react-hot-toast'
import { LogOut } from 'lucide-react'
import { backendApi, errorMessage, type Server, type Session } from '@/lib/api'
import { sessionSince, sessionState } from '@/lib/sessions'

/** SessionRows is the compact per-account session list shown under an expanded account row. */
export default function SessionRows({
  server, sessions, canTerminate,
}: {
  server: Server
  sessions: Session[]
  canTerminate: boolean
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const words = (key: string) => t(`backend.sessions.${server.protocol}.${key}`)

  const terminate = useMutation({
    mutationFn: (s: Session) => backendApi.terminateSession(server.id, s.id, s.account_id),
    onSuccess: () => {
      toast.success(words('terminated'))
      queryClient.invalidateQueries({ queryKey: ['sessions', server.id] })
      queryClient.invalidateQueries({ queryKey: ['account-sessions', server.id] })
      queryClient.invalidateQueries({ queryKey: ['server-stats', server.id] })
    },
    onError: (error) => toast.error(errorMessage(error) ?? t('backend.sessions.terminateFailed')),
  })

  if (sessions.length === 0) {
    return <p className="text-sm text-gray-400">{words('empty')}</p>
  }
  const head = 'px-3 py-2 text-left font-medium text-gray-300'

  return (
    <div className="rounded border border-gray-700 overflow-hidden">
      <table className="w-full text-sm">
        <thead className="bg-gray-700/50">
          <tr>
            <th className={head}>{words('name')}</th>
            <th className={head}>{t('backend.sessions.ip')}</th>
            <th className={head}>{words('since')}</th>
            <th className={head}>{t('common.status')}</th>
            {canTerminate && <th className={`${head} text-right w-20`}>{t('common.actions')}</th>}
          </tr>
        </thead>
        <tbody className="divide-y divide-gray-700">
          {sessions.map((s) => (
            <tr key={s.id} className="hover:bg-gray-700/30">
              <td className="px-3 py-2 font-mono text-xs text-gray-200">{s.name || s.id}</td>
              <td className="px-3 py-2 font-mono text-xs text-gray-300">{s.ip || '—'}</td>
              <td className="px-3 py-2 text-gray-400">{sessionSince(s)}</td>
              <td className="px-3 py-2 text-gray-300">{sessionState(s, t, words)}</td>
              {canTerminate && (
                <td className="px-3 py-2 text-right">
                  <button onClick={() => terminate.mutate(s)} className="p-1 text-yellow-400 hover:text-yellow-300" title={words('terminate')} aria-label={words('terminate')}>
                    <LogOut className="w-4 h-4" />
                  </button>
                </td>
              )}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
