import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { auditApi } from '@/lib/api'
import {
  FileText,
  Download,
  CheckCircle,
  XCircle,
  RefreshCw,
  Filter,
} from 'lucide-react'
import clsx from 'clsx'
import toast from 'react-hot-toast'

interface AuditLog {
  id: number
  username: string
  action: string
  resource_type?: string
  resource_id?: string
  details?: string
  ip_address?: string
  created_at: string
}

interface AuditResponse {
  data: AuditLog[]
  total: number
  limit: number
  offset: number
}

const actionColors: Record<string, string> = {
  'auth.login': 'text-green-400',
  'auth.login_failed': 'text-red-400',
  'auth.logout': 'text-gray-400',
  'user.create': 'text-blue-400',
  'user.update': 'text-yellow-400',
  'user.delete': 'text-red-400',
  'server.add': 'text-blue-400',
  'server.update': 'text-yellow-400',
  'server.remove': 'text-red-400',
  'xmpp.user_create': 'text-blue-400',
  'xmpp.user_delete': 'text-red-400',
  'xmpp.user_kick': 'text-orange-400',
}

export default function AuditLogs() {
  const { t } = useTranslation()
  const [filters, setFilters] = useState({
    action: '',
    username: '',
  })
  const [page, setPage] = useState(0)
  const limit = 50

  const { data, isLoading, refetch } = useQuery({
    queryKey: ['audit-logs', filters, page],
    queryFn: async () => {
      const response = await auditApi.list({
        ...filters,
        limit,
        offset: page * limit,
      })
      return response.data as AuditResponse
    },
  })

  const { data: verifyResult, refetch: verifyChain, isFetching: verifying } = useQuery({
    queryKey: ['audit-verify'],
    queryFn: async () => {
      const response = await auditApi.verify()
      return response.data
    },
    enabled: false,
  })

  const handleExport = async () => {
    try {
      const response = await auditApi.export(filters)
      const blob = new Blob([response.data], { type: 'text/csv' })
      const url = window.URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = `audit_logs_${new Date().toISOString().slice(0, 10)}.csv`
      a.click()
      window.URL.revokeObjectURL(url)
      toast.success(t('audit.exportSuccess'))
    } catch {
      toast.error(t('audit.exportError'))
    }
  }

  const handleVerify = async () => {
    await verifyChain()
    if (verifyResult?.valid) {
      toast.success(t('audit.verifySuccess'))
    } else {
      toast.error(t('audit.verifyFailure'))
    }
  }

  const logs = data?.data || []
  const total = data?.total || 0
  const totalPages = Math.ceil(total / limit)

  return (
    <div className="space-y-6">
      {/* Page header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold text-white">{t('audit.title')}</h1>
          <p className="text-gray-400 mt-1">{t('audit.subtitle')}</p>
        </div>
        <div className="flex gap-2">
          <button
            onClick={handleVerify}
            disabled={verifying}
            className="btn btn-secondary flex items-center gap-2"
          >
            {verifying ? (
              <RefreshCw className="w-4 h-4 animate-spin" />
            ) : verifyResult?.valid === true ? (
              <CheckCircle className="w-4 h-4 text-green-400" />
            ) : verifyResult?.valid === false ? (
              <XCircle className="w-4 h-4 text-red-400" />
            ) : (
              <CheckCircle className="w-4 h-4" />
            )}
            {t('audit.verify.button')}
          </button>
          <button onClick={handleExport} className="btn btn-secondary flex items-center gap-2">
            <Download className="w-4 h-4" />
            {t('audit.export.button')}
          </button>
        </div>
      </div>

      {/* Filters */}
      <div className="card">
        <div className="flex items-center gap-4">
          <Filter className="w-5 h-5 text-gray-400" />
          <select
            className="input w-48"
            value={filters.action}
            onChange={(e) => {
              setFilters({ ...filters, action: e.target.value })
              setPage(0)
            }}
          >
            <option value="">{t('audit.allActions')}</option>
            {[
              'auth.login',
              'auth.login_failed',
              'auth.logout',
              'user.create',
              'user.update',
              'user.delete',
              'server.add',
              'server.update',
              'server.remove',
              'xmpp.user_create',
              'xmpp.user_delete',
              'xmpp.user_kick',
            ].map((action) => (
              <option key={action} value={action}>
                {t(`audit.actions.${action}`, { defaultValue: action })}
              </option>
            ))}
          </select>
          <input
            type="text"
            className="input w-48"
            placeholder={t('audit.filterByUser')}
            value={filters.username}
            onChange={(e) => {
              setFilters({ ...filters, username: e.target.value })
              setPage(0)
            }}
          />
          <button onClick={() => refetch()} className="btn btn-secondary">
            <RefreshCw className="w-4 h-4" />
          </button>
        </div>
      </div>

      {/* Logs table */}
      <div className="card">
        {isLoading ? (
          <div className="flex items-center justify-center py-12">
            <div className="animate-spin rounded-full h-8 w-8 border-b-2 border-primary-500" />
          </div>
        ) : logs.length === 0 ? (
          <div className="text-center py-12">
            <FileText className="w-12 h-12 text-gray-600 mx-auto mb-4" />
            <p className="text-gray-400">{t('audit.noLogs')}</p>
          </div>
        ) : (
          <>
            <div className="overflow-x-auto">
              <table className="w-full">
                <thead>
                  <tr className="border-b border-gray-700">
                    <th className="table-header">{t('audit.timestamp')}</th>
                    <th className="table-header">{t('audit.user')}</th>
                    <th className="table-header">{t('audit.action')}</th>
                    <th className="table-header">{t('audit.resource')}</th>
                    <th className="table-header">{t('audit.ipAddress')}</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-gray-700">
                  {logs.map((log) => (
                    <tr key={log.id} className="hover:bg-gray-700/50">
                      <td className="table-cell text-gray-400">
                        {new Date(log.created_at).toLocaleString()}
                      </td>
                      <td className="table-cell">{log.username}</td>
                      <td className="table-cell">
                        <span className={clsx('font-mono text-sm', actionColors[log.action] || 'text-gray-300')}>
                          {log.action}
                        </span>
                      </td>
                      <td className="table-cell text-gray-400">
                        {log.resource_type && (
                          <span>
                            {log.resource_type}
                            {log.resource_id && `: ${log.resource_id}`}
                          </span>
                        )}
                      </td>
                      <td className="table-cell text-gray-400 font-mono text-sm">
                        {log.ip_address}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>

            {/* Pagination */}
            <div className="flex items-center justify-between mt-4 pt-4 border-t border-gray-700">
              <p className="text-sm text-gray-400">
                {t('audit.showingCount', {
                  from: page * limit + 1,
                  to: Math.min((page + 1) * limit, total),
                  total,
                })}
              </p>
              <div className="flex gap-2">
                <button
                  onClick={() => setPage(Math.max(0, page - 1))}
                  disabled={page === 0}
                  className="btn btn-secondary text-sm"
                >
                  {t('common.previous')}
                </button>
                <button
                  onClick={() => setPage(Math.min(totalPages - 1, page + 1))}
                  disabled={page >= totalPages - 1}
                  className="btn btn-secondary text-sm"
                >
                  {t('common.next')}
                </button>
              </div>
            </div>
          </>
        )}
      </div>
    </div>
  )
}
