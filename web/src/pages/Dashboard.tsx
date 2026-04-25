import { useQuery, useQueries } from '@tanstack/react-query'
import { serversApi } from '@/lib/api'
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

  return (
    <div className="space-y-6">
      {/* Page header */}
      <div>
        <h1 className="text-2xl font-bold text-white">Dashboard</h1>
        <p className="text-gray-400 mt-1">Overview of your XMPP infrastructure</p>
      </div>

      {/* Stats overview */}
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
        <StatCard
          icon={Server}
          label="Total Servers"
          value={servers?.length || 0}
          color="blue"
        />
        <StatCard
          icon={Activity}
          label="Active Servers"
          value={enabledServers.length}
          color="green"
        />
        <StatCard
          icon={Users}
          label="Online Users"
          value={allStatsResolved ? aggregateStats.online_users : '...'}
          color="purple"
        />
        <StatCard
          icon={MessageSquare}
          label="Active Sessions"
          value={allStatsResolved ? aggregateStats.active_sessions : '...'}
          color="orange"
        />
      </div>

      {/* Servers list */}
      <div className="card">
        <div className="flex items-center justify-between mb-4">
          <h2 className="text-lg font-semibold text-white">XMPP Servers</h2>
        </div>

        {serversLoading ? (
          <div className="flex items-center justify-center py-12">
            <div className="animate-spin rounded-full h-8 w-8 border-b-2 border-primary-500" />
          </div>
        ) : servers?.length === 0 ? (
          <div className="text-center py-12">
            <Server className="w-12 h-12 text-gray-600 mx-auto mb-4" />
            <p className="text-gray-400">No servers configured</p>
            <p className="text-gray-500 text-sm mt-1">
              Add a server to get started
            </p>
          </div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full">
              <thead>
                <tr className="border-b border-gray-700">
                  <th className="table-header">Name</th>
                  <th className="table-header">Type</th>
                  <th className="table-header">Host</th>
                  <th className="table-header">Status</th>
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
          title="Add Server"
          description="Configure a new XMPP server"
          href="/servers"
          icon={Server}
        />
        <QuickAction
          title="Manage Users"
          description="View and manage system users"
          href="/users"
          icon={Users}
        />
        <QuickAction
          title="View Audit Logs"
          description="Review system activity"
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
          <span className="badge badge-gray">Disabled</span>
        ) : isError ? (
          <span className="badge badge-red flex items-center gap-1">
            <AlertCircle className="w-3 h-3" />
            Error
          </span>
        ) : stats ? (
          <span className="badge badge-green">
            {stats.online_users} online
          </span>
        ) : (
          <span className="badge badge-yellow">Checking...</span>
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
    <a
      href={href}
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
    </a>
  )
}
