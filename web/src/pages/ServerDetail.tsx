import { useEffect, useState } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation, Trans } from 'react-i18next'
import { serversApi, xmppApi, ServerCapabilities } from '@/lib/api'
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
  const [showImportCsvModal, setShowImportCsvModal] = useState(false)
  const [pendingDeleteUser, setPendingDeleteUser] = useState<XMPPUser | null>(null)
  const [pendingBulkDelete, setPendingBulkDelete] = useState<XMPPUser[] | null>(null)
  const [selectedJids, setSelectedJids] = useState<Set<string>>(new Set())

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

  // Capabilities — what this adapter/server actually supports. Drives which
  // tabs and stat tiles render. Cache for 5 minutes; capabilities are
  // effectively static per server record.
  const { data: caps } = useQuery({
    queryKey: ['server-caps', serverId],
    queryFn: async () => {
      const response = await serversApi.capabilities(serverId)
      return response.data as ServerCapabilities
    },
    enabled: !!server?.enabled,
    staleTime: 5 * 60 * 1000,
  })

  // If the active tab gets hidden by capability filtering (e.g. user
  // navigated to a different server), fall back to users.
  useEffect(() => {
    if (!caps) return
    if (activeTab === 'sessions' && !caps.sessions) setActiveTab('users')
    if (activeTab === 'rooms' && !caps.rooms) setActiveTab('users')
  }, [caps, activeTab])

  // Auto-fill the user list domain with the server's configured XMPP host
  // so the list loads as soon as the page mounts. The user can still edit
  // the box; clearing it falls back to the server host on next mount.
  useEffect(() => {
    if (server?.host && !domain) setDomain(server.host)
  }, [server, domain])

  const { data: users, isLoading: usersLoading } = useQuery({
    queryKey: ['xmpp-users', serverId, domain],
    queryFn: async () => {
      if (!domain) return []
      const response = await xmppApi.listUsers(serverId, domain)
      return response.data as XMPPUser[]
    },
    enabled: activeTab === 'users' && !!domain,
  })

  // Clear selection whenever the user list identity changes (domain switch
  // or query invalidation). Prevents stale checkboxes pointing at JIDs no
  // longer in the table.
  useEffect(() => {
    setSelectedJids(new Set())
  }, [serverId, domain])

  const { data: sessions, isLoading: sessionsLoading } = useQuery({
    queryKey: ['xmpp-sessions', serverId],
    queryFn: async () => {
      const response = await xmppApi.listSessions(serverId)
      return response.data as XMPPSession[]
    },
    enabled: activeTab === 'sessions' && caps?.sessions === true,
    refetchInterval: 10000,
  })

  const { data: rooms, isLoading: roomsLoading } = useQuery({
    queryKey: ['xmpp-rooms', serverId, mucDomain],
    queryFn: async () => {
      if (!mucDomain) return []
      const response = await xmppApi.listRooms(serverId, mucDomain)
      return response.data as XMPPRoom[]
    },
    enabled: activeTab === 'rooms' && !!mucDomain && caps?.rooms === true,
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

  // Bulk delete: serial requests with a small concurrency cap. Per-request
  // errors are accumulated and surfaced in one toast; UI stays responsive.
  const runBulkDelete = async (targets: XMPPUser[]) => {
    setPendingBulkDelete(null)
    let ok = 0
    let failed = 0
    const concurrency = 4
    let i = 0
    const worker = async () => {
      while (i < targets.length) {
        const idx = i++
        const u = targets[idx]
        try {
          await xmppApi.deleteUser(serverId, u.username, u.domain)
          ok++
        } catch {
          failed++
        }
      }
    }
    await Promise.all(Array.from({ length: concurrency }, worker))
    queryClient.invalidateQueries({ queryKey: ['xmpp-users', serverId, domain] })
    setSelectedJids(new Set())
    if (failed === 0) {
      toast.success(t('xmpp.users.bulkDeleteSuccess', { count: ok }))
    } else {
      toast.error(t('xmpp.users.bulkDeletePartial', { ok, failed }))
    }
  }

  // CSV import: parse a flat "username,domain,password" CSV in the browser
  // and create users one at a time. Same concurrency / error-aggregation
  // pattern as bulk delete. Lines starting with '#' or empty are skipped;
  // a header row "username,domain,password" is auto-detected and skipped.
  const runCsvImport = async (csvText: string) => {
    const lines = csvText.split(/\r?\n/).map((l) => l.trim()).filter(Boolean)
    const rows: { username: string; domain: string; password: string }[] = []
    for (const line of lines) {
      if (line.startsWith('#')) continue
      const parts = line.split(',').map((p) => p.trim())
      if (parts.length < 3) continue
      const [u, d, p] = parts
      if (u.toLowerCase() === 'username' && d.toLowerCase() === 'domain') continue // header
      if (!u || !d || !p) continue
      rows.push({ username: u, domain: d, password: p })
    }
    if (rows.length === 0) {
      toast.error(t('xmpp.users.importCsvNoRows'))
      return
    }

    setShowImportCsvModal(false)
    let ok = 0
    let failed = 0
    let i = 0
    const concurrency = 4
    const worker = async () => {
      while (i < rows.length) {
        const r = rows[i++]
        try {
          await xmppApi.createUser(serverId, r)
          ok++
        } catch {
          failed++
        }
      }
    }
    await Promise.all(Array.from({ length: concurrency }, worker))
    queryClient.invalidateQueries({ queryKey: ['xmpp-users', serverId, domain] })
    if (failed === 0) {
      toast.success(t('xmpp.users.importCsvSuccess', { count: ok }))
    } else {
      toast.error(t('xmpp.users.importCsvPartial', { ok, failed }))
    }
  }

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

      {/* Stats — only show tiles the adapter reports as supported. We wait
          for both stats and caps to land so the grid doesn't flash empty. */}
      {stats && caps && (
        <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
          {caps.online_users_count && (
            <StatCard icon={Users} label={t('servers.stats.onlineUsers')} value={stats.online_users} />
          )}
          {caps.registered_users_count && (
            <StatCard icon={Users} label={t('servers.stats.registeredUsers')} value={stats.registered_users} />
          )}
          {caps.active_sessions_count && (
            <StatCard icon={Activity} label={t('dashboard.activeSessions')} value={stats.active_sessions} />
          )}
          {caps.s2s_connections_count && (
            <StatCard icon={MessageSquare} label={t('servers.stats.s2sConnections')} value={stats.s2s_connections} />
          )}
        </div>
      )}

      {/* Tabs — sessions and rooms only render if the adapter supports them. */}
      <div className="border-b border-gray-700">
        <div className="flex gap-4">
          <TabButton
            active={activeTab === 'users'}
            onClick={() => setActiveTab('users')}
            icon={Users}
            label={t('xmpp.tabUsers')}
          />
          {caps?.sessions && (
            <TabButton
              active={activeTab === 'sessions'}
              onClick={() => setActiveTab('sessions')}
              icon={Activity}
              label={t('xmpp.tabSessions')}
            />
          )}
          {caps?.rooms && (
            <TabButton
              active={activeTab === 'rooms'}
              onClick={() => setActiveTab('rooms')}
              icon={MessageSquare}
              label={t('xmpp.tabRooms')}
            />
          )}
        </div>
      </div>

      {/* Tab content */}
      <div className="card">
        {activeTab === 'users' && (
          <UsersTab
            users={users || []}
            loading={usersLoading}
            domain={domain}
            onAddUser={() => setShowAddUserModal(true)}
            onImportCsv={() => setShowImportCsvModal(true)}
            onKickUser={(u) => kickUserMutation.mutate({ username: u.username, domain: u.domain })}
            onDeleteUser={(u) => setPendingDeleteUser(u)}
            selectedJids={selectedJids}
            onSelectionChange={setSelectedJids}
            onBulkDelete={(targets) => setPendingBulkDelete(targets)}
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

      {/* Bulk delete confirmation */}
      <ConfirmDialog
        open={pendingBulkDelete !== null}
        title={t('xmpp.users.bulkDeleteTitle')}
        message={
          <Trans
            i18nKey="xmpp.users.bulkDeletePrompt"
            values={{ count: pendingBulkDelete?.length ?? 0 }}
            components={{ strong: <span className="font-semibold text-white" /> }}
          />
        }
        confirmLabel={t('common.delete')}
        cancelLabel={t('common.cancel')}
        variant="danger"
        onConfirm={() => {
          if (pendingBulkDelete) runBulkDelete(pendingBulkDelete)
        }}
        onCancel={() => setPendingBulkDelete(null)}
      />

      {/* Import CSV modal */}
      {showImportCsvModal && (
        <ImportCsvModal
          onClose={() => setShowImportCsvModal(false)}
          onImport={runCsvImport}
        />
      )}
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
  users, loading, domain,
  onAddUser, onImportCsv, onKickUser, onDeleteUser,
  selectedJids, onSelectionChange, onBulkDelete,
}: {
  users: XMPPUser[]
  loading: boolean
  domain: string
  onAddUser: () => void
  onImportCsv: () => void
  onKickUser: (u: XMPPUser) => void
  onDeleteUser: (u: XMPPUser) => void
  selectedJids: Set<string>
  onSelectionChange: (s: Set<string>) => void
  onBulkDelete: (targets: XMPPUser[]) => void
}) {
  const { t } = useTranslation()
  const [filter, setFilter] = useState('')
  const selectedCount = selectedJids.size

  // Client-side filter on the loaded user list. mod_http_admin_api
  // doesn't support server-side filtering and the list size is always
  // small enough to filter in the browser.
  const visibleUsers = filter
    ? users.filter((u) =>
        u.username.toLowerCase().includes(filter.toLowerCase()) ||
        u.jid.toLowerCase().includes(filter.toLowerCase())
      )
    : users
  // "Select all" semantics now operate on the *visible* (filtered) rows so
  // a partial filter + select-all only acts on what the operator sees.
  const allSelected = visibleUsers.length > 0 && visibleUsers.every((u) => selectedJids.has(u.jid))
  const partiallySelected = selectedCount > 0 && !allSelected

  const toggleAll = () => {
    const next = new Set(selectedJids)
    if (allSelected) {
      visibleUsers.forEach((u) => next.delete(u.jid))
    } else {
      visibleUsers.forEach((u) => next.add(u.jid))
    }
    onSelectionChange(next)
  }

  const toggleOne = (jid: string) => {
    const next = new Set(selectedJids)
    if (next.has(jid)) next.delete(jid)
    else next.add(jid)
    onSelectionChange(next)
  }

  const triggerBulkDelete = () => {
    // Only delete what's currently visible AND selected — protects against
    // stale selections from an earlier filter.
    const targets = visibleUsers.filter((u) => selectedJids.has(u.jid))
    if (targets.length > 0) onBulkDelete(targets)
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-4 gap-4 flex-wrap">
        <div className="flex items-center gap-3 text-sm text-gray-400">
          <span>{t('xmpp.users.domainLabel')}:</span>
          <code className="px-2 py-1 rounded bg-gray-700 text-gray-200">{domain || '—'}</code>
          <input
            type="text"
            className="input w-56"
            placeholder={t('xmpp.users.filterPlaceholder')}
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            aria-label={t('xmpp.users.filterPlaceholder')}
          />
        </div>
        <div className="flex items-center gap-2">
          {selectedCount > 0 && (
            <button
              onClick={triggerBulkDelete}
              className="btn btn-secondary flex items-center gap-2 text-red-400 hover:text-red-300"
            >
              <Trash2 className="w-4 h-4" />
              {t('xmpp.users.bulkDelete', { count: selectedCount })}
            </button>
          )}
          <button onClick={onImportCsv} className="btn btn-secondary flex items-center gap-2">
            <UserPlus className="w-4 h-4" />
            {t('xmpp.users.importCsv')}
          </button>
          <button onClick={onAddUser} className="btn btn-primary flex items-center gap-2">
            <UserPlus className="w-4 h-4" />
            {t('xmpp.users.addUser')}
          </button>
        </div>
      </div>

      {loading ? (
        <div className="flex justify-center py-8">
          <div className="animate-spin rounded-full h-6 w-6 border-b-2 border-primary-500" />
        </div>
      ) : users.length === 0 ? (
        <p className="text-center text-gray-400 py-8">{t('xmpp.users.noUsers')}</p>
      ) : visibleUsers.length === 0 ? (
        <p className="text-center text-gray-400 py-8">{t('xmpp.users.filterNoMatch')}</p>
      ) : (
        <table className="w-full">
          <thead>
            <tr className="border-b border-gray-700">
              <th className="table-header w-10">
                <input
                  type="checkbox"
                  className="w-4 h-4 rounded bg-gray-700 border-gray-600 text-primary-600 focus:ring-primary-500"
                  checked={allSelected}
                  ref={(el) => {
                    if (el) el.indeterminate = partiallySelected
                  }}
                  onChange={toggleAll}
                  aria-label={t('xmpp.users.selectAll')}
                />
              </th>
              <th className="table-header">{t('xmpp.users.jid')}</th>
              <th className="table-header">{t('common.status')}</th>
              <th className="table-header">{t('common.actions')}</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-700">
            {visibleUsers.map((user) => (
              <tr key={user.jid} className="hover:bg-gray-700/50">
                <td className="table-cell">
                  <input
                    type="checkbox"
                    className="w-4 h-4 rounded bg-gray-700 border-gray-600 text-primary-600 focus:ring-primary-500"
                    checked={selectedJids.has(user.jid)}
                    onChange={() => toggleOne(user.jid)}
                    aria-label={user.jid}
                  />
                </td>
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

/** Import-CSV modal: lets the operator paste / upload a flat CSV of
 *  XMPP users to bulk-create. Validation is intentionally permissive —
 *  the parent component re-validates each row before sending and surfaces
 *  the per-row failure count. */
function ImportCsvModal({
  onClose,
  onImport,
}: {
  onClose: () => void
  onImport: (csv: string) => void | Promise<void>
}) {
  const { t } = useTranslation()
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)

  const onFile = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    if (!file) return
    const content = await file.text()
    setText(content)
  }

  const submit = async () => {
    if (!text.trim()) return
    setBusy(true)
    try {
      await onImport(text)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/50">
      <div className="w-full max-w-xl bg-gray-800 rounded-xl border border-gray-700">
        <div className="flex items-center justify-between p-4 border-b border-gray-700">
          <h2 className="text-lg font-semibold text-white">{t('xmpp.users.importCsvTitle')}</h2>
          <button onClick={onClose} className="text-gray-400 hover:text-white">
            <X className="w-5 h-5" />
          </button>
        </div>

        <div className="p-4 space-y-3">
          <p className="text-sm text-gray-400">
            <Trans
              i18nKey="xmpp.users.importCsvHelp"
              components={{ code: <code className="bg-gray-700 px-1 rounded text-xs" /> }}
            />
          </p>

          <input
            type="file"
            accept=".csv,text/csv,text/plain"
            onChange={onFile}
            className="text-sm text-gray-300 file:mr-3 file:px-3 file:py-1 file:rounded file:border-0 file:bg-gray-700 file:text-white"
          />

          <textarea
            className="input font-mono text-sm h-48 resize-y"
            placeholder={'username,domain,password\nalice,example.com,Strongpw123\nbob,example.com,AnotherPw456'}
            value={text}
            onChange={(e) => setText(e.target.value)}
          />

          <div className="flex justify-end gap-2 pt-2">
            <button onClick={onClose} className="btn btn-secondary" disabled={busy}>
              {t('common.cancel')}
            </button>
            <button
              onClick={submit}
              disabled={busy || !text.trim()}
              className="btn btn-primary"
            >
              {busy ? '...' : t('xmpp.users.importCsvConfirm')}
            </button>
          </div>
        </div>
      </div>
    </div>
  )
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
