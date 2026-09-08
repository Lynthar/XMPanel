import { Outlet, NavLink, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import toast from 'react-hot-toast'
import { useAuthStore } from '@/store/auth'
import { authApi } from '@/lib/api'
import {
  LayoutDashboard,
  Server,
  Users,
  FileText,
  Settings,
  LogOut,
  Menu,
  X,
  Globe,
} from 'lucide-react'
import { useState } from 'react'
import clsx from 'clsx'

const navigationItems = [
  { key: 'dashboard', href: '/', icon: LayoutDashboard },
  { key: 'servers', href: '/servers', icon: Server },
  { key: 'users', href: '/users', icon: Users },
  { key: 'audit', href: '/audit', icon: FileText },
  { key: 'settings', href: '/settings', icon: Settings },
]

export default function Layout() {
  const { t, i18n } = useTranslation()
  const { user, logout } = useAuthStore()
  const navigate = useNavigate()
  const [sidebarOpen, setSidebarOpen] = useState(false)

  // Only the server call deletes the session row and expires the refresh
  // cookie; clearing the store alone lets the next page load sign back in.
  // Local state is cleared even when the call fails: the user asked to leave.
  const handleLogout = async () => {
    try {
      await authApi.logout()
    } catch {
      toast.error(t('auth.logoutFailed'))
    } finally {
      logout()
      navigate('/login')
    }
  }

  const toggleLanguage = () => {
    const newLang = i18n.language === 'en' ? 'zh' : 'en'
    i18n.changeLanguage(newLang)
  }

  return (
    <div className="min-h-screen bg-gray-900">
      {/* Mobile sidebar backdrop */}
      {sidebarOpen && (
        <div
          className="fixed inset-0 z-40 bg-black/50 lg:hidden"
          onClick={() => setSidebarOpen(false)}
        />
      )}

      {/* Sidebar */}
      <aside
        className={clsx(
          'fixed inset-y-0 left-0 z-50 w-64 bg-gray-800 border-r border-gray-700 transform transition-transform duration-200 lg:translate-x-0',
          sidebarOpen ? 'translate-x-0' : '-translate-x-full'
        )}
      >
        <div className="flex flex-col h-full">
          {/* Logo */}
          <div className="flex items-center justify-between h-16 px-6 border-b border-gray-700">
            <span className="text-xl font-bold text-white">XMPanel</span>
            <button
              className="lg:hidden text-gray-400 hover:text-white"
              onClick={() => setSidebarOpen(false)}
            >
              <X className="w-6 h-6" />
            </button>
          </div>

          {/* Navigation */}
          <nav className="flex-1 px-4 py-4 space-y-1 overflow-y-auto">
            {navigationItems.map((item) => (
              <NavLink
                key={item.key}
                to={item.href}
                className={({ isActive }) =>
                  clsx('sidebar-link', isActive && 'sidebar-link-active')
                }
                onClick={() => setSidebarOpen(false)}
              >
                <item.icon className="w-5 h-5" />
                {t(`nav.${item.key}`)}
              </NavLink>
            ))}
          </nav>

          {/* User section */}
          <div className="p-4 border-t border-gray-700">
            <div className="flex items-center gap-3 px-4 py-3">
              <div className="w-10 h-10 rounded-full bg-primary-600 flex items-center justify-center">
                <span className="text-white font-medium">
                  {user?.username.charAt(0).toUpperCase()}
                </span>
              </div>
              <div className="flex-1 min-w-0">
                <p className="text-sm font-medium text-white truncate">
                  {user?.username}
                </p>
                <p className="text-xs text-gray-400 truncate">{user?.role}</p>
              </div>
            </div>
            {/* Language switcher */}
            <button
              onClick={toggleLanguage}
              className="sidebar-link w-full text-gray-400 hover:text-white"
            >
              <Globe className="w-5 h-5" />
              {i18n.language === 'en' ? '中文' : 'English'}
            </button>
            <button
              onClick={handleLogout}
              className="sidebar-link w-full text-red-400 hover:text-red-300 hover:bg-red-900/20"
            >
              <LogOut className="w-5 h-5" />
              {t('nav.logout')}
            </button>
          </div>
        </div>
      </aside>

      {/* Main content */}
      <div className="lg:pl-64">
        {/* Top bar */}
        <header className="sticky top-0 z-30 h-16 bg-gray-800/95 backdrop-blur border-b border-gray-700 lg:hidden">
          <div className="flex items-center justify-between h-full px-4">
            <button
              className="text-gray-400 hover:text-white"
              onClick={() => setSidebarOpen(true)}
            >
              <Menu className="w-6 h-6" />
            </button>
            <span className="text-lg font-bold text-white">XMPanel</span>
            <div className="w-6" />
          </div>
        </header>

        {/* Page content */}
        <main className="p-6">
          <Outlet />
        </main>
      </div>
    </div>
  )
}
