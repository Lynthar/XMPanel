import { useQuery, useQueries } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { serversApi, type Server, type Stats } from '@/lib/api'
import { Server as ServerIcon, Users, MessageSquare, Activity, AlertCircle } from 'lucide-react'
import clsx from 'clsx'

export default function Dashboard() {
  const { t } = useTranslation()

  const { data: servers, isLoading: serversLoading } = useQuery({
    queryKey: ['servers'],
    queryFn: async () => (await serversApi.list()).data as Server[],
  })
  const enabledServers = servers?.filter((s) => s.enabled) || []

  // One stats query per enabled server, keyed the same way ServerRow keys
  // its own so the cache entry is shared.
  const statsResults = useQueries({
    queries: enabledServers.map((server) => ({
      queryKey: ['server-stats', server.id],
      queryFn: async () => (await serversApi.stats(server.id)).data as Stats,
      refetchInterval: 30000,
      retry: false,
    })),
  })

  // A counter aggregates only over servers that report it; when none does,
  // the tile shows a dash rather than a misleading zero.
  const sum = (pick: (s: Stats) => number | null): number | null => {
    let total: number | null = null
    for (const q of statsResults) {
      const value = q.data ? pick(q.data) : null
      if (value !== null) total = (total ?? 0) + value
    }
    return total
  }
  const settled = statsResults.every((q) => !q.isLoading)
  const tile = (value: number | null) => (settled ? value ?? '—' : '...')

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold text-white">{t('dashboard.title')}</h1>
        <p className="text-gray-400 mt-1">{t('dashboard.subtitle')}</p>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
        <StatCard icon={ServerIcon} label={t('dashboard.totalServers')} value={servers?.length || 0} color="blue" />
        <StatCard icon={Activity} label={t('dashboard.activeServers')} value={enabledServers.length} color="green" />
        <StatCard icon={Users} label={t('dashboard.onlineUsers')} value={tile(sum((s) => s.online_users))} color="purple" />
        <StatCard icon={MessageSquare} label={t('dashboard.activeSessions')} value={tile(sum((s) => s.active_sessions))} color="orange" />
      </div>

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
            <ServerIcon className="w-12 h-12 text-gray-600 mx-auto mb-4" />
            <p className="text-gray-400">{t('dashboard.noServers')}</p>
            <p className="text-gray-500 text-sm mt-1">{t('dashboard.addFirstServerHint')}</p>
          </div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full">
              <thead>
                <tr className="border-b border-gray-700">
                  <th className="table-header">{t('common.name')}</th>
                  <th className="table-header">{t('servers.implementation')}</th>
                  <th className="table-header">{t('servers.domain')}</th>
                  <th className="table-header">{t('common.status')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-700">
                {servers?.map((server) => <ServerRow key={server.id} server={server} />)}
              </tbody>
            </table>
          </div>
        )}
      </div>

      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
        <QuickAction title={t('dashboard.addServer')} description={t('dashboard.addServerDesc')} href="/servers" icon={ServerIcon} />
        <QuickAction title={t('dashboard.manageUsers')} description={t('dashboard.manageUsersDesc')} href="/users" icon={Users} />
        <QuickAction title={t('dashboard.viewAuditLogs')} description={t('dashboard.viewAuditLogsDesc')} href="/audit" icon={Activity} />
      </div>
    </div>
  )
}

function StatCard({ icon: Icon, label, value, color }: {
  icon: React.ElementType
  label: string
  value: number | string
  color: 'blue' | 'green' | 'purple' | 'orange'
}) {
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

function ServerRow({ server }: { server: Server }) {
  const { t } = useTranslation()
  const { data: stats, isError } = useQuery({
    queryKey: ['server-stats', server.id],
    queryFn: async () => (await serversApi.stats(server.id)).data as Stats,
    enabled: server.enabled,
    refetchInterval: 30000,
    retry: false,
  })

  return (
    <tr className="hover:bg-gray-700/50">
      <td className="table-cell font-medium text-white">
        <Link to={`/servers/${server.id}`} className="hover:text-primary-400">{server.name}</Link>
      </td>
      <td className="table-cell">
        <span className="badge badge-gray">{t(`servers.implementations.${server.implementation}`)}</span>
      </td>
      <td className="table-cell">{server.domain}</td>
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
            {stats.online_users !== null
              ? t('dashboard.onlineCount', { count: stats.online_users })
              : stats.registered_users !== null
                ? t('dashboard.registeredCount', { count: stats.registered_users })
                : t('dashboard.reachable')}
          </span>
        ) : (
          <span className="badge badge-yellow">{t('dashboard.checking')}</span>
        )}
      </td>
    </tr>
  )
}

function QuickAction({ title, description, href, icon: Icon }: {
  title: string
  description: string
  href: string
  icon: React.ElementType
}) {
  return (
    <Link to={href} className="card hover:border-primary-500/50 transition-colors group">
      <div className="flex items-start gap-4">
        <div className="p-2 rounded-lg bg-gray-700 group-hover:bg-primary-600/20 transition-colors">
          <Icon className="w-5 h-5 text-gray-400 group-hover:text-primary-400 transition-colors" />
        </div>
        <div>
          <h3 className="font-medium text-white group-hover:text-primary-400 transition-colors">{title}</h3>
          <p className="text-sm text-gray-400 mt-1">{description}</p>
        </div>
      </div>
    </Link>
  )
}
