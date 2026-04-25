import { useEffect, useState } from 'react'
import { Routes, Route, Navigate } from 'react-router-dom'
import { useAuthStore } from '@/store/auth'
import { authApi } from '@/lib/api'
import Layout from '@/components/Layout'
import Login from '@/pages/Login'
import Dashboard from '@/pages/Dashboard'
import Servers from '@/pages/Servers'
import ServerDetail from '@/pages/ServerDetail'
import Users from '@/pages/Users'
import AuditLogs from '@/pages/AuditLogs'
import Settings from '@/pages/Settings'

// Module-scoped guard so simultaneous mounts (StrictMode, parallel route
// resolutions) only run a single bootstrap call.
let bootstrapPromise: Promise<void> | null = null

function bootstrapAuth(): Promise<void> {
  if (bootstrapPromise) return bootstrapPromise
  bootstrapPromise = (async () => {
    try {
      const { data } = await authApi.refresh()
      const me = await authApi.me()
      useAuthStore.getState().setAuth(me.data, data.access_token)
    } catch {
      // No valid refresh cookie — caller falls through to /login.
    }
  })()
  return bootstrapPromise
}

function ProtectedRoute({ children }: { children: React.ReactNode }) {
  const isAuthenticated = useAuthStore((s) => s.isAuthenticated)
  const [bootstrapping, setBootstrapping] = useState(!isAuthenticated)

  useEffect(() => {
    if (isAuthenticated) {
      setBootstrapping(false)
      return
    }
    bootstrapAuth().finally(() => setBootstrapping(false))
  }, [isAuthenticated])

  if (bootstrapping) {
    return (
      <div className="min-h-screen flex items-center justify-center bg-gray-900">
        <div className="animate-spin rounded-full h-10 w-10 border-b-2 border-primary-500" />
      </div>
    )
  }

  if (!isAuthenticated) {
    return <Navigate to="/login" replace />
  }

  return <>{children}</>
}

function App() {
  return (
    <Routes>
      <Route path="/login" element={<Login />} />
      <Route
        path="/"
        element={
          <ProtectedRoute>
            <Layout />
          </ProtectedRoute>
        }
      >
        <Route index element={<Dashboard />} />
        <Route path="servers" element={<Servers />} />
        <Route path="servers/:id" element={<ServerDetail />} />
        <Route path="users" element={<Users />} />
        <Route path="audit" element={<AuditLogs />} />
        <Route path="settings" element={<Settings />} />
      </Route>
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  )
}

export default App
