import { useEffect, useState } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useQuery, useMutation } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import toast from 'react-hot-toast'
import { ArrowLeft, Users, MessageSquare, Activity, RefreshCw, AlertTriangle, Clock, Globe } from 'lucide-react'
import clsx from 'clsx'
import {
  serversApi, errorMessage,
  type Capability, type Server, type ServerCapabilities, type Stats,
} from '@/lib/api'
import Accounts from './Accounts'
import Sessions from './Sessions'
import Rooms from './Rooms'

type Tab = 'accounts' | 'sessions' | 'rooms'

const tabCapability: Record<Tab, Capability> = {
  accounts: 'accounts.list',
  sessions: 'sessions.list_all',
  rooms: 'rooms.list',
}

/** Server page shell: header, the counters the backend can report, and one tab per declared capability. */
export default function ServerDetail() {
  const { t } = useTranslation()
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const serverId = parseInt(id!, 10)
  const [activeTab, setActiveTab] = useState<Tab>('accounts')

  const { data: server, isLoading: serverLoading } = useQuery({
    queryKey: ['server', serverId],
    queryFn: async () => (await serversApi.get(serverId)).data as Server,
  })

  const capsQuery = useQuery({
    queryKey: ['server-caps', serverId],
    queryFn: async () => (await serversApi.capabilities(serverId)).data as ServerCapabilities,
    enabled: !!server?.enabled,
    staleTime: 5 * 60 * 1000,
    retry: false,
  })
  const caps = capsQuery.data
  const has = (c: Capability) => caps?.capabilities.includes(c) ?? false

  const { data: stats, refetch: refetchStats } = useQuery({
    queryKey: ['server-stats', serverId],
    queryFn: async () => (await serversApi.stats(serverId)).data as Stats,
    enabled: !!caps,
    refetchInterval: 30000,
  })

  const testMutation = useMutation({
    mutationFn: () => serversApi.test(serverId),
    onSuccess: (response) => {
      if (response.data.success) {
        toast.success(t('servers.testSuccess'))
        capsQuery.refetch()
      } else {
        toast.error(`${t('servers.testFailed')}: ${response.data.error}`)
      }
    },
    onError: () => toast.error(t('servers.testError')),
  })

  // A tab whose capability the server lacks falls back to the first offered one.
  const tabs = (['accounts', 'sessions', 'rooms'] as Tab[]).filter((tab) => has(tabCapability[tab]))
  useEffect(() => {
    if (caps && !tabs.includes(activeTab) && tabs.length > 0) setActiveTab(tabs[0])
  }, [caps, tabs, activeTab])

  if (serverLoading) {
    return (
      <div className="flex items-center justify-center py-12">
        <div className="animate-spin rounded-full h-8 w-8 border-b-2 border-primary-500" />
      </div>
    )
  }
  if (!server) {
    return <p className="text-center text-gray-400 py-12">{t('backend.noServerFound')}</p>
  }

  const protocol = server.protocol
  const tabIcons = { accounts: Users, sessions: Activity, rooms: MessageSquare }

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-4">
        <button onClick={() => navigate('/servers')} className="p-2 text-gray-400 hover:text-white rounded-lg hover:bg-gray-800" aria-label={t('common.back')}>
          <ArrowLeft className="w-5 h-5" />
        </button>
        <div className="flex-1 min-w-0">
          <h1 className="text-2xl font-bold text-white">{server.name}</h1>
          <p className="text-gray-400 truncate">
            {t(`servers.implementations.${server.implementation}`)} · {server.domain} · {server.endpoint}
            {caps?.info.version && <> · {caps.info.version}</>}
          </p>
        </div>
        <button onClick={() => testMutation.mutate()} disabled={testMutation.isPending} className="btn btn-secondary flex items-center gap-2">
          <RefreshCw className={clsx('w-4 h-4', testMutation.isPending && 'animate-spin')} />
          {t('servers.testConnection')}
        </button>
        <button onClick={() => refetchStats()} className="btn btn-secondary flex items-center gap-2">
          <RefreshCw className="w-4 h-4" />
          {t('common.refresh')}
        </button>
      </div>

      {!server.enabled ? (
        <p className="card text-gray-400">{t('backend.serverDisabled')}</p>
      ) : capsQuery.isError ? (
        <div className="card flex items-start gap-3 border-red-900/50">
          <AlertTriangle className="w-5 h-5 text-red-400 mt-0.5" />
          <div>
            <p className="text-white font-medium">{t('backend.probeFailed')}</p>
            <p className="text-gray-400 text-sm mt-1">{errorMessage(capsQuery.error) ?? t('errors.generic')}</p>
          </div>
        </div>
      ) : (
        <>
          {stats && (
            <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
              <StatCard icon={Users} label={t('servers.stats.registeredUsers')} value={stats.registered_users} />
              <StatCard icon={Users} label={t('servers.stats.onlineUsers')} value={stats.online_users} />
              <StatCard icon={Activity} label={t(`backend.sessions.${protocol}.stat`)} value={stats.active_sessions} />
              <StatCard icon={MessageSquare} label={t('servers.stats.rooms')} value={stats.rooms} />
              <StatCard icon={Globe} label={t('servers.stats.s2sConnections')} value={stats.s2s_connections} />
              <StatCard icon={Clock} label={t('servers.stats.uptime')} value={formatUptime(stats.uptime_seconds)} />
            </div>
          )}

          <div className="border-b border-gray-700">
            <div className="flex gap-4">
              {tabs.map((tab) => {
                const Icon = tabIcons[tab]
                return (
                  <button
                    key={tab}
                    onClick={() => setActiveTab(tab)}
                    className={clsx(
                      'flex items-center gap-2 px-4 py-3 border-b-2 transition-colors',
                      activeTab === tab ? 'border-primary-500 text-primary-400' : 'border-transparent text-gray-400 hover:text-white'
                    )}
                  >
                    <Icon className="w-4 h-4" />
                    {tab === 'sessions' ? t(`backend.sessions.${protocol}.tab`) : t(`backend.${tab}.tab`)}
                  </button>
                )
              })}
            </div>
          </div>

          <div className="card">
            {caps && tabs.length === 0 && <p className="text-center text-gray-400 py-8">{t('backend.noCapabilities')}</p>}
            {caps && activeTab === 'accounts' && has('accounts.list') && <Accounts server={server} caps={caps} />}
            {caps && activeTab === 'sessions' && has('sessions.list_all') && <Sessions server={server} caps={caps} />}
            {caps && activeTab === 'rooms' && has('rooms.list') && <Rooms server={server} caps={caps} />}
          </div>
        </>
      )}
    </div>
  )
}

function formatUptime(seconds: number | null): string | null {
  if (seconds === null) return null
  const days = Math.floor(seconds / 86400)
  const hours = Math.floor((seconds % 86400) / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  if (days > 0) return `${days}d ${hours}h`
  if (hours > 0) return `${hours}h ${minutes}m`
  return `${minutes}m`
}

/** A counter tile; null renders as a dash because the backend cannot report it. */
function StatCard({ icon: Icon, label, value }: { icon: React.ElementType; label: string; value: number | string | null }) {
  return (
    <div className="card flex items-center gap-3">
      <Icon className="w-5 h-5 text-gray-400" />
      <div>
        <p className="text-xl font-bold text-white">{value ?? '—'}</p>
        <p className="text-xs text-gray-400">{label}</p>
      </div>
    </div>
  )
}
