import axios, { AxiosError, InternalAxiosRequestConfig } from 'axios'
import { useAuthStore } from '@/store/auth'

const CSRF_COOKIE = 'csrf_token'

// Read a cookie value by name. The CSRF cookie is intentionally not HttpOnly
// (server-side at internal/api/handler/auth.go) so we can mirror it into the
// X-CSRF-Token header — the double-submit pattern that the CSRFMiddleware
// validates on /auth/refresh.
function readCookie(name: string): string | undefined {
  const match = document.cookie.match(new RegExp(`(^|; )${name}=([^;]+)`))
  return match ? decodeURIComponent(match[2]) : undefined
}

const api = axios.create({
  baseURL: '/api/v1',
  headers: {
    'Content-Type': 'application/json',
  },
  // Required for the browser to attach the xmpanel_refresh HttpOnly cookie on
  // /auth/refresh and the csrf_token cookie on every request, both for
  // same-origin and CORS contexts.
  withCredentials: true,
})

// Request interceptor: attach Bearer access token + mirror CSRF cookie.
api.interceptors.request.use(
  (config: InternalAxiosRequestConfig) => {
    const { accessToken } = useAuthStore.getState()
    if (accessToken) {
      config.headers.Authorization = `Bearer ${accessToken}`
    }

    const method = (config.method || 'get').toUpperCase()
    if (method !== 'GET' && method !== 'HEAD' && method !== 'OPTIONS') {
      const csrf = readCookie(CSRF_COOKIE)
      if (csrf) {
        config.headers['X-CSRF-Token'] = csrf
      }
    }
    return config
  },
  (error) => Promise.reject(error)
)

// Response interceptor: silently exchange the refresh cookie for a new access
// token on 401 and replay the original request once.
api.interceptors.response.use(
  (response) => response,
  async (error: AxiosError) => {
    const originalRequest = error.config as InternalAxiosRequestConfig & { _retry?: boolean }
    if (!originalRequest || error.response?.status !== 401 || originalRequest._retry) {
      return Promise.reject(error)
    }

    // Don't try to refresh on the refresh endpoint itself or on login —
    // both are loops/no-ops.
    const url = originalRequest.url || ''
    if (url.includes('/auth/refresh') || url.includes('/auth/login')) {
      return Promise.reject(error)
    }

    originalRequest._retry = true
    try {
      const { data } = await api.post('/auth/refresh')
      const accessToken: string = data.access_token
      useAuthStore.getState().setAccessToken(accessToken)
      originalRequest.headers.Authorization = `Bearer ${accessToken}`
      return api(originalRequest)
    } catch {
      useAuthStore.getState().logout()
      return Promise.reject(error)
    }
  }
)

// Auth API
export const authApi = {
  login: (username: string, password: string, totp_code?: string, recovery_code?: string) =>
    api.post('/auth/login', { username, password, totp_code, recovery_code }),

  logout: () => api.post('/auth/logout'),

  // Refresh has no body — the browser ships the xmpanel_refresh HttpOnly cookie.
  refresh: () => api.post('/auth/refresh'),

  me: () => api.get('/auth/me'),

  setupMFA: () => api.post('/auth/mfa/setup'),

  verifyMFA: (code: string) => api.post('/auth/mfa/verify', { code }),

  disableMFA: (password: string, code: string) =>
    api.post('/auth/mfa/disable', { password, code }),

  changePassword: (currentPassword: string, newPassword: string) =>
    api.post('/auth/password', { current_password: currentPassword, new_password: newPassword }),
}

// Users API
export const usersApi = {
  list: () => api.get('/users'),

  get: (id: number) => api.get(`/users/${id}`),

  create: (data: { username: string; email: string; password: string; role: string }) =>
    api.post('/users', data),

  update: (id: number, data: { email?: string; password?: string; role?: string }) =>
    api.put(`/users/${id}`, data),

  delete: (id: number) => api.delete(`/users/${id}`),
}

// Copies of Go tables. A Go test keeps each one in step with its constants:
// implementations with adapter.Implementation, Capability with
// adapter.AllCapabilities, the interfaces below with the json tags.
export type Protocol = 'xmpp' | 'matrix'

export const implementations = {
  prosody: { protocol: 'xmpp' as Protocol },
  ejabberd: { protocol: 'xmpp' as Protocol },
  synapse: { protocol: 'matrix' as Protocol },
  tuwunel: { protocol: 'matrix' as Protocol },
  'matrix-generic': { protocol: 'matrix' as Protocol },
} as const

export type Implementation = keyof typeof implementations

export const protocols: Protocol[] = ['xmpp', 'matrix']

/** Implementations that belong to a protocol, in declaration order. */
export function implementationsFor(protocol: Protocol): Implementation[] {
  return (Object.keys(implementations) as Implementation[]).filter((impl) => implementations[impl].protocol === protocol)
}

export type Capability =
  | 'accounts.list' | 'accounts.get' | 'accounts.search' | 'accounts.create' | 'accounts.delete'
  | 'accounts.set_password' | 'accounts.set_enabled' | 'accounts.set_admin'
  | 'sessions.list_all' | 'sessions.list_by_account' | 'sessions.terminate'
  | 'rooms.list' | 'rooms.get' | 'rooms.create' | 'rooms.delete'
  | 'matrix.deactivate' | 'matrix.suspend' | 'matrix.shadow_ban' | 'matrix.registration_tokens' | 'matrix.reports'
  | 'matrix.media' | 'matrix.media_quarantine' | 'matrix.room_block' | 'matrix.room_purge' | 'matrix.server_notice' | 'matrix.federation'

export interface MASCredentials {
  endpoint: string
  client_id: string
  client_secret: string
}

export interface Credentials {
  kind: string
  token?: string
  mas?: MASCredentials
}

export interface User {
  id: number
  username: string
  email: string
  role: string
  mfa_enabled: boolean
  locked_until?: string
  last_login_at?: string
  last_login_ip?: string
  created_at: string
  updated_at: string
}

export interface Server {
  id: number
  name: string
  protocol: Protocol
  implementation: Implementation
  endpoint: string
  domain: string
  enabled: boolean
  created_at: string
  updated_at: string
}

export interface CreateServerRequest {
  name: string
  protocol: Protocol
  implementation: Implementation
  endpoint: string
  domain: string
  credentials: Credentials
}

export interface ServerInfo {
  protocol: Protocol
  implementation: Implementation
  version: string
  domains: string[]
  auth_mode?: string
}

export interface ServerCapabilities {
  info: ServerInfo
  capabilities: Capability[]
}

export interface Stats {
  version: string
  uptime_seconds: number | null
  registered_users: number | null
  online_users: number | null
  active_sessions: number | null
  rooms: number | null
  s2s_connections: number | null
}

// One bucket of sampled history. ok_ratio is the share of samples in the
// bucket that reached the server; the counters are the bucket's last reading.
export interface Point {
  ts: string
  ok_ratio: number
  latency_ms: number | null
  registered_users: number | null
  online_users: number | null
  active_sessions: number | null
  rooms: number | null
}

// from/to bound the x axis even where no sample exists, so a gap in the data
// renders as a gap rather than a straight line across it.
export interface Series {
  from: string
  to: string
  bucket_seconds: number
  points: Point[]
}

export type CheckStatus = 'ok' | 'warn' | 'fail'

export interface CheckItem {
  target: string
  status: CheckStatus
  values?: string[]
  not_after?: string
  error?: string
}

export interface CheckDetail {
  items: CheckItem[]
}

export interface Check {
  kind: string
  ts: string
  status: CheckStatus
  detail: CheckDetail
}

export const sampleRanges = ['1h', '6h', '24h', '7d', '30d'] as const
export type SampleRange = (typeof sampleRanges)[number]

export interface XMPPAccountFacts {
  roles?: string[]
}

export interface MatrixAccountFacts {
  deactivated: boolean
  locked: boolean
  suspended?: boolean
  shadow_banned: boolean
  erased: boolean
  user_type?: string
}

export interface Account {
  id: string
  localpart: string
  domain: string
  display_name?: string
  enabled: boolean
  admin: boolean
  created_at?: string
  last_seen?: string
  matrix?: MatrixAccountFacts
  xmpp?: XMPPAccountFacts
}

export interface CreateAccountRequest {
  localpart: string
  domain?: string
  password: string
  display_name?: string
  admin?: boolean
}

export interface XMPPSessionFacts {
  priority: number
  status?: string
}

export interface Session {
  id: string
  account_id: string
  name?: string
  ip?: string
  user_agent?: string
  started_at?: string
  last_seen?: string
  live: boolean
  xmpp?: XMPPSessionFacts
}

export interface XMPPRoomFacts {
  description?: string
  persistent: boolean
  members_only: boolean
  moderated: boolean
}

export interface MatrixRoomFacts {
  version: string
  creator?: string
  encryption?: string
  federatable: boolean
  joined_local_members: number
  topic?: string
}

export interface Room {
  id: string
  name?: string
  alias?: string
  members: number
  public: boolean
  matrix?: MatrixRoomFacts
  xmpp?: XMPPRoomFacts
}

export interface RegistrationToken {
  token: string
  uses_allowed: number | null
  used: number
  pending: number
  expires_at?: string
  created_at?: string
  valid: boolean
  revoked: boolean
}

export interface CreateRegistrationTokenRequest {
  token?: string
  uses_allowed?: number
  expires_at?: string
}

export interface EventReport {
  id: string
  received_at: string
  room_id: string
  room_name?: string
  room_alias?: string
  event_id: string
  reporter: string
  sender: string
  reason?: string
  score: number
}

export interface Media {
  id: string
  type?: string
  size: number
  name?: string
  created_at?: string
  last_access?: string
  quarantined: boolean
  protected: boolean
}

export interface PurgeRoomRequest {
  block: boolean
  purge: boolean
  force_purge: boolean
  new_room_user?: string
  new_room_name?: string
  message?: string
}

export interface FederationDestination {
  destination: string
  healthy: boolean
  last_failure_at?: string
  failing_since?: string
  retry_interval_ms: number
}

export interface CreateRoomRequest {
  name: string
  domain?: string
  description?: string
  public: boolean
  persistent: boolean
  members_only: boolean
}

export interface Page<T> {
  items: T[]
  next?: string
  total?: number
}

export interface ListParams {
  search?: string
  domain?: string
  limit?: number
  cursor?: string
}

// Servers API
export const serversApi = {
  list: () => api.get('/servers'),

  get: (id: number) => api.get(`/servers/${id}`),

  create: (data: CreateServerRequest) => api.post('/servers', data),

  update: (
    id: number,
    data: { name?: string; endpoint?: string; domain?: string; credentials?: Credentials; enabled?: boolean }
  ) => api.put(`/servers/${id}`, data),

  delete: (id: number) => api.delete(`/servers/${id}`),

  stats: (id: number) => api.get(`/servers/${id}/stats`),

  samples: (id: number, range: SampleRange) => api.get(`/servers/${id}/samples`, { params: { range } }),

  checks: (id: number) => api.get(`/servers/${id}/checks`),

  capabilities: (id: number) => api.get(`/servers/${id}/capabilities`),

  test: (id: number) => api.post(`/servers/${id}/test`),
}

// Backend objects behind a registered server. Account and session ids are
// JIDs or MXIDs, so they travel URL-encoded as one path segment.
const seg = encodeURIComponent

export const backendApi = {
  listAccounts: (serverId: number, params?: ListParams) =>
    api.get(`/servers/${serverId}/accounts`, { params }),

  getAccount: (serverId: number, account: string) =>
    api.get(`/servers/${serverId}/accounts/${seg(account)}`),

  createAccount: (serverId: number, data: CreateAccountRequest) =>
    api.post(`/servers/${serverId}/accounts`, data),

  deleteAccount: (serverId: number, account: string) =>
    api.delete(`/servers/${serverId}/accounts/${seg(account)}`),

  setPassword: (serverId: number, account: string, password: string) =>
    api.put(`/servers/${serverId}/accounts/${seg(account)}/password`, { password }),

  setEnabled: (serverId: number, account: string, enabled: boolean) =>
    api.put(`/servers/${serverId}/accounts/${seg(account)}/enabled`, { enabled }),

  setAdmin: (serverId: number, account: string, admin: boolean) =>
    api.put(`/servers/${serverId}/accounts/${seg(account)}/admin`, { admin }),

  listAccountSessions: (serverId: number, account: string) =>
    api.get(`/servers/${serverId}/accounts/${seg(account)}/sessions`),

  terminateAccountSessions: (serverId: number, account: string) =>
    api.delete(`/servers/${serverId}/accounts/${seg(account)}/sessions`),

  listSessions: (serverId: number, params?: ListParams) =>
    api.get(`/servers/${serverId}/sessions`, { params }),

  terminateSession: (serverId: number, session: string, account: string) =>
    api.delete(`/servers/${serverId}/sessions/${seg(session)}`, { params: { account } }),

  listRooms: (serverId: number, params?: ListParams) =>
    api.get(`/servers/${serverId}/rooms`, { params }),

  getRoom: (serverId: number, room: string) =>
    api.get(`/servers/${serverId}/rooms/${seg(room)}`),

  createRoom: (serverId: number, data: CreateRoomRequest) =>
    api.post(`/servers/${serverId}/rooms`, data),

  deleteRoom: (serverId: number, room: string) =>
    api.delete(`/servers/${serverId}/rooms/${seg(room)}`),
}

// Matrix moderation; a server without it answers 501 on every call.
export const matrixApi = {
  deactivate: (serverId: number, account: string, erase: boolean) =>
    api.post(`/servers/${serverId}/matrix/accounts/${seg(account)}/deactivate`, { erase }),

  setSuspended: (serverId: number, account: string, suspended: boolean) =>
    api.put(`/servers/${serverId}/matrix/accounts/${seg(account)}/suspended`, { suspended }),

  setShadowBanned: (serverId: number, account: string, banned: boolean) =>
    api.put(`/servers/${serverId}/matrix/accounts/${seg(account)}/shadow-banned`, { banned }),

  listAccountMedia: (serverId: number, account: string, params?: ListParams) =>
    api.get(`/servers/${serverId}/matrix/accounts/${seg(account)}/media`, { params }),

  quarantineAccountMedia: (serverId: number, account: string) =>
    api.post(`/servers/${serverId}/matrix/accounts/${seg(account)}/media/quarantine`),

  deleteMedia: (serverId: number, mediaId: string) =>
    api.delete(`/servers/${serverId}/matrix/media/${seg(mediaId)}`),

  listRegistrationTokens: (serverId: number) =>
    api.get(`/servers/${serverId}/matrix/registration-tokens`),

  createRegistrationToken: (serverId: number, data: CreateRegistrationTokenRequest) =>
    api.post(`/servers/${serverId}/matrix/registration-tokens`, data),

  deleteRegistrationToken: (serverId: number, token: string) =>
    api.delete(`/servers/${serverId}/matrix/registration-tokens/${seg(token)}`),

  listReports: (serverId: number, params?: ListParams) =>
    api.get(`/servers/${serverId}/matrix/reports`, { params }),

  blockRoom: (serverId: number, room: string, block: boolean) =>
    api.post(`/servers/${serverId}/matrix/rooms/${seg(room)}/block`, { block }),

  purgeRoom: (serverId: number, room: string, data: PurgeRoomRequest) =>
    api.post(`/servers/${serverId}/matrix/rooms/${seg(room)}/purge`, data),

  sendNotice: (serverId: number, account: string, body: string) =>
    api.post(`/servers/${serverId}/matrix/notices`, { account, body }),

  listFederation: (serverId: number, params?: ListParams) =>
    api.get(`/servers/${serverId}/matrix/federation`, { params }),
}

// Audit API
export const auditApi = {
  list: (params?: {
    user_id?: number
    username?: string
    action?: string
    resource_type?: string
    start_time?: string
    end_time?: string
    details_contains?: string
    limit?: number
    offset?: number
  }) => api.get('/audit', { params }),

  verify: (startId?: number, endId?: number) =>
    api.get('/audit/verify', { params: { start_id: startId, end_id: endId } }),

  export: (params?: { action?: string; start_time?: string; end_time?: string }) =>
    api.get('/audit/export', { params, responseType: 'blob' }),
}

/**
 * Message for a failed list query. The sidebar shows every page to every
 * role, so a viewer opening Users gets a 403 — which has to read as "not
 * allowed", never as an empty list.
 *
 * @param error - the error react-query surfaced
 * @param t - i18next translator
 * @returns a translated, user-facing sentence
 */
export function listErrorMessage(error: unknown, t: (key: string) => string): string {
  const status = (error as { response?: { status?: number } } | null)?.response?.status
  if (status === 403) return t('errors.forbidden')
  if (status === 401) return t('errors.unauthorized')
  return t('errors.generic')
}

/** Whether a request failed because the resource does not exist. */
export function isNotFound(error: unknown): boolean {
  return (error as { response?: { status?: number } } | null)?.response?.status === 404
}

/** The backend's translated error message, if the response carried one. */
export function errorMessage(error: unknown): string | undefined {
  return (error as { response?: { data?: { error?: string } } } | null)?.response?.data?.error
}

export default api
