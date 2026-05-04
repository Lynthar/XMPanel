import { useQuery, useQueries } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { serversApi, ServerCapabilities } from '@/lib/api'
import { Server, Users, MessageSquare, Activity, AlertCircle } from 'lucide-react'
import clsx from 'clsx'

interface ServerStats {
  online_users: number
  registered_users: number
  active_sessions: number
  s2s_connections: number
}

interface ServerData {
  id: number
  name: string
  type: string
  host: string
  enabled: boolean
}

export default function Dashboard() {
  const { t } = useTranslation()

  const { data: servers, isLoading: serversLoading } = useQuery({
    queryKey: ['servers'],
    queryFn: async () => {
      const response = await serversApi.list()
      return response.data as ServerData[]
    },
  })

  // Get stats for each enabled server
  const enabledServers = servers?.filter((s) => s.enabled) || []

  // Aggregate stats across all enabled servers. Each query is also keyed by
  // server id so ServerRow below shares the same cache entry — no duplicate fetches.
  const statsResults = useQueries({
    queries: enabledServers.map((server) => ({
      queryKey: ['server-stats', server.id],
      queryFn: async () => {
        const response = await serversApi.stats(server.id)
        return response.data as ServerStats
      },
      refetchInterval: 30000,
    })),
  })

  // Capabilities decide whether each aggregated stat is meaningful. If no
  // server reports a capability, we render "—" instead of a misleading 0.
  const capsResults = useQueries({
    queries: enabledServers.map((server) => ({
      queryKey: ['server-caps', server.id],
      queryFn: async () => {
        const response = await serversApi.capabilities(server.id)
        return response.data as ServerCapabilities
      },
      staleTime: 5 * 60 * 1000,
    })),
  })

  const anyCap = (key: keyof ServerCapabilities) =>
    capsResults.some((q) => q.data?.[key])

  const aggregateStats = statsResults.reduce(
    (acc, q) => {
      if (q.data) {
        acc.online_users += q.data.online_users || 0
        acc.active_sessions += q.data.active_sessions || 0
      }
      return acc
    },
    { online_users: 0, active_sessions: 0 }
  )
  const allStatsResolved = statsResults.length === 0 || statsResults.every((q) => !q.isLoading)
  const allCapsResolved = capsResults.length === 0 || capsResults.every((q) => !q.isLoading)
  const showOnlineUsers = !allCapsResolved || anyCap('online_users_count')
  const showActiveSessions = !allCapsResolved || anyCap('active_sessions_count')

  return (
    <div className="space-y-6">
      {/* Page header */}
      <div>
        <h1 className="text-2xl font-bold text-white">{t('dashboard.title')}</h1>
        <p className="text-gray-400 mt-1">{t('dashboard.subtitle')}</p>
      </div>

      {/* Stats overview */}
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
        <StatCard
          icon={Server}
          label={t('dashboard.totalServers')}
          value={servers?.length || 0}
          color="blue"
        />
        <StatCard
          icon={Activity}
          label={t('dashboard.activeServers')}
          value={enabledServers.length}
          color="green"
        />
        <StatCard
          icon={Users}
          label={t('dashboard.onlineUsers')}
          value={
            !showOnlineUsers
              ? '—'
              : allStatsResolved
              ? aggregateStats.online_users
              : '...'
          }
          color="purple"
        />
        <StatCard
          icon={MessageSquare}
          label={t('dashboard.activeSessions')}
          value={
            !showActiveSessions
              ? '—'
              : allStatsResolved
              ? aggregateStats.active_sessions
              : '...'
          }
          color="orange"
        />
      </div>

      {/* Servers list */}
      <div className="card">
        <div className="flex items-center justify-between mb-4">
          <h2 className="text-lg font-semibold text-white">{t('dashboard.serversList')}</h2>
        </div>

        {serversLoading ? (
          <div className="flex items-center justify-center py-12">
            <div className="animate-spin rounded-full h-8 w-8 border-b-2 border-primary-500" />
          </div>
        ) : servers?.length === 0 ? (
          <div className="text-center py-12">
            <Server className="w-12 h-12 text-gray-600 mx-auto mb-4" />
            <p className="text-gray-400">{t('dashboard.noServers')}</p>
            <p className="text-gray-500 text-sm mt-1">{t('dashboard.addFirstServerHint')}</p>
          </div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full">
              <thead>
                <tr className="border-b border-gray-700">
                  <th className="table-header">{t('common.name')}</th>
                  <th className="table-header">{t('common.type')}</th>
                  <th className="table-header">{t('servers.hostname')}</th>
                  <th className="table-header">{t('common.status')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-700">
                {servers?.map((server) => (
                  <ServerRow key={server.id} server={server} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {/* Quick actions */}
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
        <QuickAction
          title={t('dashboard.addServer')}
          description={t('dashboard.addServerDesc')}
          href="/servers"
          icon={Server}
        />
        <QuickAction
          title={t('dashboard.manageUsers')}
          description={t('dashboard.manageUsersDesc')}
          href="/users"
          icon={Users}
        />
        <QuickAction
          title={t('dashboard.viewAuditLogs')}
          description={t('dashboard.viewAuditLogsDesc')}
          href="/audit"
          icon={Activity}
        />
      </div>
    </div>
  )
}

interface StatCardProps {
  icon: React.ElementType
  label: string
  value: number | string
  color: 'blue' | 'green' | 'purple' | 'orange'
}

function StatCard({ icon: Icon, label, value, color }: StatCardProps) {
  const colorClasses = {
    blue: 'bg-blue-900/30 text-blue-400',
    green: 'bg-green-900/30 text-green-400',
    purple: 'bg-purple-900/30 text-purple-400',
    orange: 'bg-orange-900/30 text-orange-400',
  }

  return (
    <div className="card flex items-center gap-4">
      <div className={clsx('p-3 rounded-lg', colorClasses[color])}>
        <Icon className="w-6 h-6" />
      </div>
      <div>
        <p className="text-2xl font-bold text-white">{value}</p>
        <p className="text-sm text-gray-400">{label}</p>
      </div>
    </div>
  )
}

function ServerRow({ server }: { server: ServerData }) {
  const { t } = useTranslation()
  const { data: stats, isError } = useQuery({
    queryKey: ['server-stats', server.id],
    queryFn: async () => {
      if (!server.enabled) return null
      const response = await serversApi.stats(server.id)
      return response.data as ServerStats
    },
    enabled: server.enabled,
    refetchInterval: 30000, // Refresh every 30 seconds
  })

  return (
    <tr className="hover:bg-gray-700/50">
      <td className="table-cell font-medium text-white">{server.name}</td>
      <td className="table-cell">
        <span className="badge badge-gray capitalize">{server.type}</span>
      </td>
      <td className="table-cell">{server.host}</td>
      <td className="table-cell">
        {!server.enabled ? (
          <span className="badge badge-gray">{t('dashboard.disabled')}</span>
        ) : isError ? (
          <span className="badge badge-red flex items-center gap-1">
            <AlertCircle className="w-3 h-3" />
            {t('dashboard.errorState')}
          </span>
        ) : stats ? (
          <span className="badge badge-green">
            {t('dashboard.onlineCount', { count: stats.online_users })}
          </span>
        ) : (
          <span className="badge badge-yellow">{t('dashboard.checking')}</span>
        )}
      </td>
    </tr>
  )
}

interface QuickActionProps {
  title: string
  description: string
  href: string
  icon: React.ElementType
}

function QuickAction({ title, description, href, icon: Icon }: QuickActionProps) {
  return (
    <Link
      to={href}
      className="card hover:border-primary-500/50 transition-colors group"
    >
      <div className="flex items-start gap-4">
        <div className="p-2 rounded-lg bg-gray-700 group-hover:bg-primary-600/20 transition-colors">
          <Icon className="w-5 h-5 text-gray-400 group-hover:text-primary-400 transition-colors" />
        </div>
        <div>
          <h3 className="font-medium text-white group-hover:text-primary-400 transition-colors">
            {title}
          </h3>
          <p className="text-sm text-gray-400 mt-1">{description}</p>
        </div>
      </div>
    </Link>
  )
}
