import { useState } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation, Trans } from 'react-i18next'
import { serversApi, xmppApi } from '@/lib/api'
import toast from 'react-hot-toast'
import {
  ArrowLeft,
  Users,
  MessageSquare,
  Activity,
  RefreshCw,
  UserPlus,
  Trash2,
  X,
  LogOut,
} from 'lucide-react'
import clsx from 'clsx'
import { useForm } from 'react-hook-form'
import ConfirmDialog from '@/components/ConfirmDialog'

interface ServerData {
  id: number
  name: string
  type: string
  host: string
  port: number
  tls_enabled: boolean
  enabled: boolean
}

interface XMPPUser {
  jid: string
  username: string
  domain: string
  online: boolean
  resources?: string[]
}

interface XMPPSession {
  jid: string
  resource: string
  ip_address: string
  priority: number
  status: string
}

interface XMPPRoom {
  jid: string
  name: string
  occupants: number
  public: boolean
  persistent: boolean
}

export default function ServerDetail() {
  const { t } = useTranslation()
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const serverId = parseInt(id!, 10)

  const [activeTab, setActiveTab] = useState<'users' | 'sessions' | 'rooms'>('users')
  const [domain, setDomain] = useState('')
  const [mucDomain, setMucDomain] = useState('')
  const [showAddUserModal, setShowAddUserModal] = useState(false)
  const [pendingDeleteUser, setPendingDeleteUser] = useState<XMPPUser | null>(null)

  const { data: server, isLoading: serverLoading } = useQuery({
    queryKey: ['server', serverId],
    queryFn: async () => {
      const response = await serversApi.get(serverId)
      return response.data as ServerData
    },
  })

  const { data: stats, refetch: refetchStats } = useQuery({
    queryKey: ['server-stats', serverId],
    queryFn: async () => {
      const response = await serversApi.stats(serverId)
      return response.data
    },
    enabled: !!server?.enabled,
    refetchInterval: 30000,
  })

  const { data: users, isLoading: usersLoading } = useQuery({
    queryKey: ['xmpp-users', serverId, domain],
    queryFn: async () => {
      if (!domain) return []
      const response = await xmppApi.listUsers(serverId, domain)
      return response.data as XMPPUser[]
    },
    enabled: activeTab === 'users' && !!domain,
  })

  const { data: sessions, isLoading: sessionsLoading } = useQuery({
    queryKey: ['xmpp-sessions', serverId],
    queryFn: async () => {
      const response = await xmppApi.listSessions(serverId)
      return response.data as XMPPSession[]
    },
    enabled: activeTab === 'sessions',
    refetchInterval: 10000,
  })

  const { data: rooms, isLoading: roomsLoading } = useQuery({
    queryKey: ['xmpp-rooms', serverId, mucDomain],
    queryFn: async () => {
      if (!mucDomain) return []
      const response = await xmppApi.listRooms(serverId, mucDomain)
      return response.data as XMPPRoom[]
    },
    enabled: activeTab === 'rooms' && !!mucDomain,
  })

  const kickUserMutation = useMutation({
    mutationFn: ({ username, domain }: { username: string; domain: string }) =>
      xmppApi.kickUser(serverId, username, domain),
    onSuccess: () => {
      toast.success(t('xmpp.users.kickSuccess'))
      queryClient.invalidateQueries({ queryKey: ['xmpp-sessions', serverId] })
    },
    onError: () => toast.error(t('xmpp.users.kickFailed')),
  })

  const deleteUserMutation = useMutation({
    mutationFn: ({ username, domain }: { username: string; domain: string }) =>
      xmppApi.deleteUser(serverId, username, domain),
    onSuccess: () => {
      toast.success(t('xmpp.users.deleteSuccess'))
      queryClient.invalidateQueries({ queryKey: ['xmpp-users', serverId, domain] })
    },
    onError: () => toast.error(t('xmpp.users.deleteFailed')),
    onSettled: () => setPendingDeleteUser(null),
  })

  if (serverLoading) {
    return (
      <div className="flex items-center justify-center py-12">
        <div className="animate-spin rounded-full h-8 w-8 border-b-2 border-primary-500" />
      </div>
    )
  }

  if (!server) {
    return (
      <div className="text-center py-12">
        <p className="text-gray-400">{t('xmpp.noServerFound')}</p>
      </div>
    )
  }

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex items-center gap-4">
        <button
          onClick={() => navigate('/servers')}
          className="p-2 text-gray-400 hover:text-white rounded-lg hover:bg-gray-800"
        >
          <ArrowLeft className="w-5 h-5" />
        </button>
        <div className="flex-1">
          <h1 className="text-2xl font-bold text-white">{server.name}</h1>
          <p className="text-gray-400">
            {server.type} • {server.host}:{server.port}
          </p>
        </div>
        <button
          onClick={() => refetchStats()}
          className="btn btn-secondary flex items-center gap-2"
        >
          <RefreshCw className="w-4 h-4" />
          {t('common.refresh')}
        </button>
      </div>

      {/* Stats */}
      {stats && (
        <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
          <StatCard icon={Users} label={t('servers.stats.onlineUsers')} value={stats.online_users} />
          <StatCard icon={Users} label={t('servers.stats.registeredUsers')} value={stats.registered_users} />
          <StatCard icon={Activity} label={t('dashboard.activeSessions')} value={stats.active_sessions} />
          <StatCard icon={MessageSquare} label={t('servers.stats.s2sConnections')} value={stats.s2s_connections} />
        </div>
      )}

      {/* Tabs */}
      <div className="border-b border-gray-700">
        <div className="flex gap-4">
          <TabButton
            active={activeTab === 'users'}
            onClick={() => setActiveTab('users')}
            icon={Users}
            label={t('xmpp.tabUsers')}
          />
          <TabButton
            active={activeTab === 'sessions'}
            onClick={() => setActiveTab('sessions')}
            icon={Activity}
            label={t('xmpp.tabSessions')}
          />
          <TabButton
            active={activeTab === 'rooms'}
            onClick={() => setActiveTab('rooms')}
            icon={MessageSquare}
            label={t('xmpp.tabRooms')}
          />
        </div>
      </div>

      {/* Tab content */}
      <div className="card">
        {activeTab === 'users' && (
          <UsersTab
            users={users || []}
            loading={usersLoading}
            domain={domain}
            onDomainChange={setDomain}
            onAddUser={() => setShowAddUserModal(true)}
            onKickUser={(u) => kickUserMutation.mutate({ username: u.username, domain: u.domain })}
            onDeleteUser={(u) => setPendingDeleteUser(u)}
          />
        )}

        {activeTab === 'sessions' && (
          <SessionsTab sessions={sessions || []} loading={sessionsLoading} />
        )}

        {activeTab === 'rooms' && (
          <RoomsTab
            rooms={rooms || []}
            loading={roomsLoading}
            mucDomain={mucDomain}
            onMucDomainChange={setMucDomain}
          />
        )}
      </div>

      {/* Add user modal */}
      {showAddUserModal && (
        <AddUserModal
          serverId={serverId}
          onClose={() => setShowAddUserModal(false)}
          onSuccess={() => {
            setShowAddUserModal(false)
            queryClient.invalidateQueries({ queryKey: ['xmpp-users', serverId, domain] })
          }}
        />
      )}

      <ConfirmDialog
        open={pendingDeleteUser !== null}
        title={t('xmpp.users.deleteTitle')}
        message={
          <Trans
            i18nKey="xmpp.users.deletePrompt"
            values={{ jid: pendingDeleteUser?.jid ?? '' }}
            components={{ strong: <span className="font-semibold text-white" /> }}
          />
        }
        confirmLabel={t('common.delete')}
        cancelLabel={t('common.cancel')}
        variant="danger"
        loading={deleteUserMutation.isPending}
        onConfirm={() => {
          if (pendingDeleteUser) {
            deleteUserMutation.mutate({
              username: pendingDeleteUser.username,
              domain: pendingDeleteUser.domain,
            })
          }
        }}
        onCancel={() => setPendingDeleteUser(null)}
      />
    </div>
  )
}

function StatCard({ icon: Icon, label, value }: { icon: React.ElementType; label: string; value: number }) {
  return (
    <div className="card flex items-center gap-3">
      <Icon className="w-5 h-5 text-gray-400" />
      <div>
        <p className="text-xl font-bold text-white">{value}</p>
        <p className="text-xs text-gray-400">{label}</p>
      </div>
    </div>
  )
}

function TabButton({ active, onClick, icon: Icon, label }: {
  active: boolean
  onClick: () => void
  icon: React.ElementType
  label: string
}) {
  return (
    <button
      onClick={onClick}
      className={clsx(
        'flex items-center gap-2 px-4 py-3 border-b-2 transition-colors',
        active
          ? 'border-primary-500 text-primary-400'
          : 'border-transparent text-gray-400 hover:text-white'
      )}
    >
      <Icon className="w-4 h-4" />
      {label}
    </button>
  )
}

function UsersTab({
  users, loading, domain, onDomainChange, onAddUser, onKickUser, onDeleteUser
}: {
  users: XMPPUser[]
  loading: boolean
  domain: string
  onDomainChange: (d: string) => void
  onAddUser: () => void
  onKickUser: (u: XMPPUser) => void
  onDeleteUser: (u: XMPPUser) => void
}) {
  const { t } = useTranslation()
  return (
    <div>
      <div className="flex items-center justify-between mb-4">
        <input
          type="text"
          className="input w-64"
          placeholder={t('xmpp.domainPlaceholder')}
          value={domain}
          onChange={(e) => onDomainChange(e.target.value)}
        />
        <button onClick={onAddUser} className="btn btn-primary flex items-center gap-2">
          <UserPlus className="w-4 h-4" />
          {t('xmpp.users.addUser')}
        </button>
      </div>

      {!domain ? (
        <p className="text-center text-gray-400 py-8">{t('xmpp.domainHint')}</p>
      ) : loading ? (
        <div className="flex justify-center py-8">
          <div className="animate-spin rounded-full h-6 w-6 border-b-2 border-primary-500" />
        </div>
      ) : users.length === 0 ? (
        <p className="text-center text-gray-400 py-8">{t('xmpp.users.noUsers')}</p>
      ) : (
        <table className="w-full">
          <thead>
            <tr className="border-b border-gray-700">
              <th className="table-header">{t('xmpp.users.jid')}</th>
              <th className="table-header">{t('common.status')}</th>
              <th className="table-header">{t('common.actions')}</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-700">
            {users.map((user) => (
              <tr key={user.jid} className="hover:bg-gray-700/50">
                <td className="table-cell">{user.jid}</td>
                <td className="table-cell">
                  <span className={clsx('badge', user.online ? 'badge-green' : 'badge-gray')}>
                    {user.online ? t('xmpp.users.online') : t('xmpp.users.offline')}
                  </span>
                </td>
                <td className="table-cell">
                  <div className="flex gap-2">
                    {user.online && (
                      <button
                        onClick={() => onKickUser(user)}
                        className="p-1 text-yellow-400 hover:text-yellow-300"
                        title={t('xmpp.users.kick')}
                      >
                        <LogOut className="w-4 h-4" />
                      </button>
                    )}
                    <button
                      onClick={() => onDeleteUser(user)}
                      className="p-1 text-red-400 hover:text-red-300"
                      title={t('common.delete')}
                    >
                      <Trash2 className="w-4 h-4" />
                    </button>
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}

function SessionsTab({ sessions, loading }: { sessions: XMPPSession[]; loading: boolean }) {
  const { t } = useTranslation()
  if (loading) {
    return (
      <div className="flex justify-center py-8">
        <div className="animate-spin rounded-full h-6 w-6 border-b-2 border-primary-500" />
      </div>
    )
  }

  if (sessions.length === 0) {
    return <p className="text-center text-gray-400 py-8">{t('xmpp.sessions.noSessions')}</p>
  }

  return (
    <table className="w-full">
      <thead>
        <tr className="border-b border-gray-700">
          <th className="table-header">{t('xmpp.sessions.jid')}</th>
          <th className="table-header">{t('xmpp.sessions.ip')}</th>
          <th className="table-header">{t('common.status')}</th>
          <th className="table-header">{t('xmpp.sessions.priority')}</th>
        </tr>
      </thead>
      <tbody className="divide-y divide-gray-700">
        {sessions.map((session, i) => (
          <tr key={i} className="hover:bg-gray-700/50">
            <td className="table-cell">{session.jid}</td>
            <td className="table-cell">{session.ip_address}</td>
            <td className="table-cell">{session.status || 'available'}</td>
            <td className="table-cell">{session.priority}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

function RoomsTab({
  rooms, loading, mucDomain, onMucDomainChange
}: {
  rooms: XMPPRoom[]
  loading: boolean
  mucDomain: string
  onMucDomainChange: (d: string) => void
}) {
  const { t } = useTranslation()
  return (
    <div>
      <div className="mb-4">
        <input
          type="text"
          className="input w-64"
          placeholder={t('xmpp.mucDomainPlaceholder')}
          value={mucDomain}
          onChange={(e) => onMucDomainChange(e.target.value)}
        />
      </div>

      {!mucDomain ? (
        <p className="text-center text-gray-400 py-8">{t('xmpp.mucDomainHint')}</p>
      ) : loading ? (
        <div className="flex justify-center py-8">
          <div className="animate-spin rounded-full h-6 w-6 border-b-2 border-primary-500" />
        </div>
      ) : rooms.length === 0 ? (
        <p className="text-center text-gray-400 py-8">{t('xmpp.rooms.noRooms')}</p>
      ) : (
        <table className="w-full">
          <thead>
            <tr className="border-b border-gray-700">
              <th className="table-header">{t('xmpp.rooms.roomName')}</th>
              <th className="table-header">{t('xmpp.rooms.occupants')}</th>
              <th className="table-header">{t('xmpp.rooms.public')}</th>
              <th className="table-header">{t('xmpp.rooms.persistent')}</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-700">
            {rooms.map((room) => (
              <tr key={room.jid} className="hover:bg-gray-700/50">
                <td className="table-cell">{room.name}</td>
                <td className="table-cell">{room.occupants}</td>
                <td className="table-cell">
                  <span className={clsx('badge', room.public ? 'badge-green' : 'badge-gray')}>
                    {room.public ? t('common.yes') : t('common.no')}
                  </span>
                </td>
                <td className="table-cell">
                  <span className={clsx('badge', room.persistent ? 'badge-blue' : 'badge-gray')}>
                    {room.persistent ? t('common.yes') : t('common.no')}
                  </span>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}

interface AddUserForm {
  username: string
  domain: string
  password: string
}

function AddUserModal({ serverId, onClose, onSuccess }: {
  serverId: number
  onClose: () => void
  onSuccess: () => void
}) {
  const { t } = useTranslation()
  const [loading, setLoading] = useState(false)
  const { register, handleSubmit, formState: { errors } } = useForm<AddUserForm>()

  const onSubmit = async (data: AddUserForm) => {
    setLoading(true)
    try {
      await xmppApi.createUser(serverId, data)
      toast.success(t('xmpp.users.createSuccess'))
      onSuccess()
    } catch (error: unknown) {
      const err = error as { response?: { data?: { error?: string } } }
      toast.error(err.response?.data?.error || t('xmpp.users.createFailed'))
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/50">
      <div className="w-full max-w-md bg-gray-800 rounded-xl border border-gray-700">
        <div className="flex items-center justify-between p-4 border-b border-gray-700">
          <h2 className="text-lg font-semibold text-white">{t('xmpp.users.addUserTitle')}</h2>
          <button onClick={onClose} className="text-gray-400 hover:text-white">
            <X className="w-5 h-5" />
          </button>
        </div>

        <form onSubmit={handleSubmit(onSubmit)} className="p-4 space-y-4">
          <div>
            <label className="block text-sm font-medium text-gray-300 mb-1">{t('xmpp.users.username')}</label>
            <input
              type="text"
              className="input"
              {...register('username', { required: t('validation.required') })}
            />
            {errors.username && <p className="mt-1 text-sm text-red-400">{errors.username.message}</p>}
          </div>

          <div>
            <label className="block text-sm font-medium text-gray-300 mb-1">{t('xmpp.users.domain')}</label>
            <input
              type="text"
              className="input"
              placeholder={t('xmpp.users.domainPlaceholder')}
              {...register('domain', { required: t('validation.required') })}
            />
            {errors.domain && <p className="mt-1 text-sm text-red-400">{errors.domain.message}</p>}
          </div>

          <div>
            <label className="block text-sm font-medium text-gray-300 mb-1">{t('auth.password')}</label>
            <input
              type="password"
              className="input"
              {...register('password', {
                required: t('validation.required'),
                minLength: { value: 8, message: t('validation.minLength', { min: 8 }) },
              })}
            />
            {errors.password && <p className="mt-1 text-sm text-red-400">{errors.password.message}</p>}
          </div>

          <div className="flex justify-end gap-3 pt-4">
            <button type="button" onClick={onClose} className="btn btn-secondary">{t('common.cancel')}</button>
            <button type="submit" disabled={loading} className="btn btn-primary">
              {loading ? '...' : t('users.createUser')}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
