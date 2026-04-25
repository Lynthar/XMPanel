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
  login: (username: string, password: string, totp_code?: string) =>
    api.post('/auth/login', { username, password, totp_code }),

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

// Servers API
export const serversApi = {
  list: () => api.get('/servers'),

  get: (id: number) => api.get(`/servers/${id}`),

  create: (data: {
    name: string
    type: string
    host: string
    port: number
    api_key: string
    tls_enabled: boolean
  }) => api.post('/servers', data),

  update: (id: number, data: { name?: string; api_key?: string; tls_enabled?: boolean; enabled?: boolean }) =>
    api.put(`/servers/${id}`, data),

  delete: (id: number) => api.delete(`/servers/${id}`),

  stats: (id: number) => api.get(`/servers/${id}/stats`),

  test: (id: number) => api.post(`/servers/${id}/test`),
}

// XMPP API
export const xmppApi = {
  // Users
  listUsers: (serverId: number, domain: string) =>
    api.get(`/servers/${serverId}/users`, { params: { domain } }),

  getUser: (serverId: number, username: string, domain: string) =>
    api.get(`/servers/${serverId}/users/${username}`, { params: { domain } }),

  createUser: (serverId: number, data: { username: string; domain: string; password: string }) =>
    api.post(`/servers/${serverId}/users`, data),

  deleteUser: (serverId: number, username: string, domain: string) =>
    api.delete(`/servers/${serverId}/users/${username}`, { params: { domain } }),

  kickUser: (serverId: number, username: string, domain: string) =>
    api.post(`/servers/${serverId}/users/${username}/kick`, null, { params: { domain } }),

  // Sessions
  listSessions: (serverId: number) => api.get(`/servers/${serverId}/sessions`),

  kickSession: (serverId: number, jid: string) =>
    api.delete(`/servers/${serverId}/sessions/${encodeURIComponent(jid)}`),

  // Rooms
  listRooms: (serverId: number, mucDomain: string) =>
    api.get(`/servers/${serverId}/rooms`, { params: { muc_domain: mucDomain } }),

  getRoom: (serverId: number, room: string, mucDomain: string) =>
    api.get(`/servers/${serverId}/rooms/${room}`, { params: { muc_domain: mucDomain } }),

  createRoom: (
    serverId: number,
    data: { name: string; domain: string; description?: string; public?: boolean; persistent?: boolean; members_only?: boolean }
  ) => api.post(`/servers/${serverId}/rooms`, data),

  deleteRoom: (serverId: number, room: string, mucDomain: string) =>
    api.delete(`/servers/${serverId}/rooms/${room}`, { params: { muc_domain: mucDomain } }),
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
    limit?: number
    offset?: number
  }) => api.get('/audit', { params }),

  verify: (startId?: number, endId?: number) =>
    api.get('/audit/verify', { params: { start_id: startId, end_id: endId } }),

  export: (params?: { action?: string; start_time?: string; end_time?: string }) =>
    api.get('/audit/export', { params, responseType: 'blob' }),
}

export default api
