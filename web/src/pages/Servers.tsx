import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { serversApi } from '@/lib/api'
import { useForm } from 'react-hook-form'
import toast from 'react-hot-toast'
import {
  Server,
  Plus,
  MoreVertical,
  Trash2,
  RefreshCw,
  ExternalLink,
  X,
} from 'lucide-react'
import clsx from 'clsx'

interface ServerData {
  id: number
  name: string
  type: string
  host: string
  port: number
  tls_enabled: boolean
  enabled: boolean
  created_at: string
}

interface CreateServerForm {
  name: string
  type: string
  host: string
  port: number
  api_key: string
  tls_enabled: boolean
}

export default function Servers() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [showAddModal, setShowAddModal] = useState(false)
  const [selectedServer, setSelectedServer] = useState<number | null>(null)

  const { data: servers, isLoading } = useQuery({
    queryKey: ['servers'],
    queryFn: async () => {
      const response = await serversApi.list()
      return response.data as ServerData[]
    },
  })

  const deleteMutation = useMutation({
    mutationFn: (id: number) => serversApi.delete(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['servers'] })
      toast.success(t('servers.deleteSuccess'))
      setSelectedServer(null)
    },
    onError: () => {
      toast.error(t('servers.deleteFailed'))
    },
  })

  const testMutation = useMutation({
    mutationFn: (id: number) => serversApi.test(id),
    onSuccess: (response) => {
      if (response.data.success) {
        toast.success(t('servers.testSuccess'))
      } else {
        toast.error(`${t('servers.testFailed')}: ${response.data.error}`)
      }
    },
    onError: () => {
      toast.error(t('servers.testError'))
    },
  })

  return (
    <div className="space-y-6">
      {/* Page header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold text-white">{t('servers.title')}</h1>
          <p className="text-gray-400 mt-1">{t('servers.subtitle')}</p>
        </div>
        <button onClick={() => setShowAddModal(true)} className="btn btn-primary flex items-center gap-2">
          <Plus className="w-4 h-4" />
          {t('servers.addServer')}
        </button>
      </div>

      {/* Servers grid */}
      {isLoading ? (
        <div className="flex items-center justify-center py-12">
          <div className="animate-spin rounded-full h-8 w-8 border-b-2 border-primary-500" />
        </div>
      ) : servers?.length === 0 ? (
        <div className="card text-center py-12">
          <Server className="w-16 h-16 text-gray-600 mx-auto mb-4" />
          <h3 className="text-lg font-medium text-white mb-2">{t('servers.noServersTitle')}</h3>
          <p className="text-gray-400 mb-6">{t('servers.noServersDesc')}</p>
          <button onClick={() => setShowAddModal(true)} className="btn btn-primary">
            {t('servers.addServer')}
          </button>
        </div>
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
          {servers?.map((server) => (
            <ServerCard
              key={server.id}
              server={server}
              onTest={() => testMutation.mutate(server.id)}
              onDelete={() => deleteMutation.mutate(server.id)}
              isSelected={selectedServer === server.id}
              onSelect={() => setSelectedServer(selectedServer === server.id ? null : server.id)}
            />
          ))}
        </div>
      )}

      {/* Add server modal */}
      {showAddModal && (
        <AddServerModal
          onClose={() => setShowAddModal(false)}
          onSuccess={() => {
            setShowAddModal(false)
            queryClient.invalidateQueries({ queryKey: ['servers'] })
          }}
        />
      )}
    </div>
  )
}

interface ServerCardProps {
  server: ServerData
  onTest: () => void
  onDelete: () => void
  isSelected: boolean
  onSelect: () => void
}

function ServerCard({ server, onTest, onDelete, isSelected, onSelect }: ServerCardProps) {
  const { t } = useTranslation()
  return (
    <div className={clsx('card relative', isSelected && 'ring-2 ring-primary-500')}>
      <div className="flex items-start justify-between mb-4">
        <div className="flex items-center gap-3">
          <div
            className={clsx(
              'p-2 rounded-lg',
              server.enabled ? 'bg-green-900/30' : 'bg-gray-700'
            )}
          >
            <Server
              className={clsx(
                'w-5 h-5',
                server.enabled ? 'text-green-400' : 'text-gray-400'
              )}
            />
          </div>
          <div>
            <h3 className="font-medium text-white">{server.name}</h3>
            <span className="badge badge-gray text-xs capitalize">{server.type}</span>
          </div>
        </div>
        <div className="relative">
          <button
            onClick={onSelect}
            className="p-1 text-gray-400 hover:text-white rounded"
          >
            <MoreVertical className="w-5 h-5" />
          </button>
          {isSelected && (
            <div className="absolute right-0 top-8 w-40 bg-gray-700 rounded-lg shadow-xl border border-gray-600 py-1 z-10">
              <Link
                to={`/servers/${server.id}`}
                className="flex items-center gap-2 px-4 py-2 text-sm text-gray-300 hover:bg-gray-600"
              >
                <ExternalLink className="w-4 h-4" />
                {t('servers.viewDetails')}
              </Link>
              <button
                onClick={onTest}
                className="flex items-center gap-2 px-4 py-2 text-sm text-gray-300 hover:bg-gray-600 w-full"
              >
                <RefreshCw className="w-4 h-4" />
                {t('servers.testConnection')}
              </button>
              <button
                onClick={onDelete}
                className="flex items-center gap-2 px-4 py-2 text-sm text-red-400 hover:bg-gray-600 w-full"
              >
                <Trash2 className="w-4 h-4" />
                {t('common.delete')}
              </button>
            </div>
          )}
        </div>
      </div>

      <div className="space-y-2 text-sm">
        <div className="flex justify-between">
          <span className="text-gray-400">{t('servers.hostname')}</span>
          <span className="text-gray-200">{server.host}</span>
        </div>
        <div className="flex justify-between">
          <span className="text-gray-400">{t('servers.port')}</span>
          <span className="text-gray-200">{server.port}</span>
        </div>
        <div className="flex justify-between">
          <span className="text-gray-400">{t('servers.tls')}</span>
          <span className={server.tls_enabled ? 'text-green-400' : 'text-gray-500'}>
            {server.tls_enabled ? t('servers.tlsEnabled') : t('servers.tlsDisabled')}
          </span>
        </div>
        <div className="flex justify-between">
          <span className="text-gray-400">{t('common.status')}</span>
          <span className={clsx('badge', server.enabled ? 'badge-green' : 'badge-gray')}>
            {server.enabled ? t('servers.active') : t('servers.tlsDisabled')}
          </span>
        </div>
      </div>

      <div className="mt-4 pt-4 border-t border-gray-700">
        <Link
          to={`/servers/${server.id}`}
          className="btn btn-secondary w-full text-center text-sm"
        >
          {t('servers.manageServer')}
        </Link>
      </div>
    </div>
  )
}

interface AddServerModalProps {
  onClose: () => void
  onSuccess: () => void
}

function AddServerModal({ onClose, onSuccess }: AddServerModalProps) {
  const { t } = useTranslation()
  const [loading, setLoading] = useState(false)
  const { register, handleSubmit, formState: { errors } } = useForm<CreateServerForm>({
    defaultValues: {
      type: 'prosody',
      port: 5280,
      tls_enabled: true,
    },
  })

  const onSubmit = async (data: CreateServerForm) => {
    setLoading(true)
    try {
      await serversApi.create(data)
      toast.success(t('servers.addSuccess'))
      onSuccess()
    } catch (error: unknown) {
      const err = error as { response?: { data?: { error?: string } } }
      toast.error(err.response?.data?.error || t('servers.addFailed'))
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/50">
      <div className="w-full max-w-lg bg-gray-800 rounded-xl border border-gray-700 shadow-xl">
        <div className="flex items-center justify-between p-4 border-b border-gray-700">
          <h2 className="text-lg font-semibold text-white">{t('servers.addServerTitle')}</h2>
          <button onClick={onClose} className="text-gray-400 hover:text-white">
            <X className="w-5 h-5" />
          </button>
        </div>

        <form onSubmit={handleSubmit(onSubmit)} className="p-4 space-y-4">
          <div>
            <label className="block text-sm font-medium text-gray-300 mb-1">{t('common.name')}</label>
            <input
              type="text"
              className="input"
              placeholder={t('servers.namePlaceholder')}
              {...register('name', { required: t('validation.required') })}
            />
            {errors.name && <p className="mt-1 text-sm text-red-400">{errors.name.message}</p>}
          </div>

          <div>
            <label className="block text-sm font-medium text-gray-300 mb-1">{t('common.type')}</label>
            <select className="input" {...register('type')}>
              <option value="prosody">Prosody</option>
              <option value="ejabberd">ejabberd</option>
            </select>
          </div>

          <div className="grid grid-cols-2 gap-4">
            <div>
              <label className="block text-sm font-medium text-gray-300 mb-1">{t('servers.hostname')}</label>
              <input
                type="text"
                className="input"
                placeholder={t('servers.hostPlaceholder')}
                {...register('host', { required: t('validation.required') })}
              />
              {errors.host && <p className="mt-1 text-sm text-red-400">{errors.host.message}</p>}
            </div>
            <div>
              <label className="block text-sm font-medium text-gray-300 mb-1">{t('servers.port')}</label>
              <input
                type="number"
                className="input"
                {...register('port', { required: t('validation.required'), valueAsNumber: true })}
              />
              {errors.port && <p className="mt-1 text-sm text-red-400">{errors.port.message}</p>}
            </div>
          </div>

          <div>
            <label className="block text-sm font-medium text-gray-300 mb-1">{t('servers.apiKey')}</label>
            <input
              type="password"
              className="input"
              placeholder={t('servers.apiKeyPlaceholder')}
              {...register('api_key', { required: t('validation.required') })}
            />
            {errors.api_key && <p className="mt-1 text-sm text-red-400">{errors.api_key.message}</p>}
          </div>

          <div className="flex items-center gap-2">
            <input
              type="checkbox"
              id="tls_enabled"
              className="w-4 h-4 rounded bg-gray-700 border-gray-600 text-primary-600 focus:ring-primary-500"
              {...register('tls_enabled')}
            />
            <label htmlFor="tls_enabled" className="text-sm text-gray-300">
              {t('servers.enableTls')}
            </label>
          </div>

          <div className="flex justify-end gap-3 pt-4 border-t border-gray-700">
            <button type="button" onClick={onClose} className="btn btn-secondary">
              {t('common.cancel')}
            </button>
            <button type="submit" disabled={loading} className="btn btn-primary">
              {loading ? '...' : t('servers.addServer')}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
