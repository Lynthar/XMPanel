import { useEffect, useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useForm } from 'react-hook-form'
import toast from 'react-hot-toast'
import { Server as ServerIcon, Plus, MoreVertical, Trash2, RefreshCw, ExternalLink } from 'lucide-react'
import clsx from 'clsx'
import {
  serversApi, errorMessage, implementationsFor, protocols,
  type CreateServerRequest, type Implementation, type Protocol, type Server,
} from '@/lib/api'
import ConfirmDialog from '@/components/ConfirmDialog'
import Modal from '@/components/Modal'
import Field from '@/components/Field'

export default function Servers() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [showAddModal, setShowAddModal] = useState(false)
  const [menuFor, setMenuFor] = useState<number | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<Server | null>(null)

  const { data: servers, isLoading } = useQuery({
    queryKey: ['servers'],
    queryFn: async () => (await serversApi.list()).data as Server[],
  })

  const deleteMutation = useMutation({
    mutationFn: (id: number) => serversApi.delete(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['servers'] })
      toast.success(t('servers.deleteSuccess'))
    },
    onError: (error) => toast.error(errorMessage(error) ?? t('servers.deleteFailed')),
    onSettled: () => setDeleteTarget(null),
  })

  const testMutation = useMutation({
    mutationFn: (id: number) => serversApi.test(id),
    onSuccess: (response, id) => {
      queryClient.invalidateQueries({ queryKey: ['server-caps', id] })
      if (response.data.success) toast.success(t('servers.testSuccess'))
      else toast.error(`${t('servers.testFailed')}: ${response.data.error}`)
    },
    onError: () => toast.error(t('servers.testError')),
  })

  return (
    <div className="space-y-6">
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

      {isLoading ? (
        <div className="flex items-center justify-center py-12">
          <div className="animate-spin rounded-full h-8 w-8 border-b-2 border-primary-500" />
        </div>
      ) : servers?.length === 0 ? (
        <div className="card text-center py-12">
          <ServerIcon className="w-16 h-16 text-gray-600 mx-auto mb-4" />
          <h3 className="text-lg font-medium text-white mb-2">{t('servers.noServersTitle')}</h3>
          <p className="text-gray-400 mb-6">{t('servers.noServersDesc')}</p>
          <button onClick={() => setShowAddModal(true)} className="btn btn-primary">{t('servers.addServer')}</button>
        </div>
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
          {servers?.map((server) => (
            <ServerCard
              key={server.id}
              server={server}
              onTest={() => testMutation.mutate(server.id)}
              onDelete={() => setDeleteTarget(server)}
              menuOpen={menuFor === server.id}
              onToggleMenu={() => setMenuFor(menuFor === server.id ? null : server.id)}
            />
          ))}
        </div>
      )}

      {showAddModal && (
        <AddServerModal
          onClose={() => setShowAddModal(false)}
          onSuccess={() => {
            setShowAddModal(false)
            queryClient.invalidateQueries({ queryKey: ['servers'] })
          }}
        />
      )}
      <ConfirmDialog
        open={deleteTarget !== null}
        title={t('servers.deleteServer')}
        message={t('servers.deleteWarning', { name: deleteTarget?.name ?? '' })}
        confirmLabel={t('common.delete')}
        cancelLabel={t('common.cancel')}
        variant="danger"
        loading={deleteMutation.isPending}
        onConfirm={() => deleteTarget && deleteMutation.mutate(deleteTarget.id)}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  )
}

function ServerCard({
  server, onTest, onDelete, menuOpen, onToggleMenu,
}: {
  server: Server
  onTest: () => void
  onDelete: () => void
  menuOpen: boolean
  onToggleMenu: () => void
}) {
  const { t } = useTranslation()
  return (
    <div className={clsx('card relative', menuOpen && 'ring-2 ring-primary-500')}>
      <div className="flex items-start justify-between mb-4">
        <div className="flex items-center gap-3 min-w-0">
          <div className={clsx('p-2 rounded-lg', server.enabled ? 'bg-green-900/30' : 'bg-gray-700')}>
            <ServerIcon className={clsx('w-5 h-5', server.enabled ? 'text-green-400' : 'text-gray-400')} />
          </div>
          <div className="min-w-0">
            <h3 className="font-medium text-white truncate">{server.name}</h3>
            <span className="badge badge-gray text-xs">{t(`servers.protocols.${server.protocol}`)}</span>
            <span className="badge badge-blue text-xs ml-1">{t(`servers.implementations.${server.implementation}`)}</span>
          </div>
        </div>
        <div className="relative">
          <button onClick={onToggleMenu} className="p-1 text-gray-400 hover:text-white rounded" aria-label={t('common.actions')}>
            <MoreVertical className="w-5 h-5" />
          </button>
          {menuOpen && (
            <div className="absolute right-0 top-8 w-44 bg-gray-700 rounded-lg shadow-xl border border-gray-600 py-1 z-10">
              <Link to={`/servers/${server.id}`} className="flex items-center gap-2 px-4 py-2 text-sm text-gray-300 hover:bg-gray-600">
                <ExternalLink className="w-4 h-4" />
                {t('servers.viewDetails')}
              </Link>
              <button onClick={onTest} className="flex items-center gap-2 px-4 py-2 text-sm text-gray-300 hover:bg-gray-600 w-full">
                <RefreshCw className="w-4 h-4" />
                {t('servers.testConnection')}
              </button>
              <button onClick={onDelete} className="flex items-center gap-2 px-4 py-2 text-sm text-red-400 hover:bg-gray-600 w-full">
                <Trash2 className="w-4 h-4" />
                {t('common.delete')}
              </button>
            </div>
          )}
        </div>
      </div>

      <div className="space-y-2 text-sm">
        <div className="flex justify-between gap-4">
          <span className="text-gray-400">{t('servers.domain')}</span>
          <span className="text-gray-200 truncate">{server.domain}</span>
        </div>
        <div className="flex justify-between gap-4">
          <span className="text-gray-400">{t('servers.endpoint')}</span>
          <span className="text-gray-200 truncate font-mono text-xs">{server.endpoint}</span>
        </div>
        <div className="flex justify-between">
          <span className="text-gray-400">{t('common.status')}</span>
          <span className={clsx('badge', server.enabled ? 'badge-green' : 'badge-gray')}>
            {server.enabled ? t('servers.active') : t('servers.disabled')}
          </span>
        </div>
      </div>

      <div className="mt-4 pt-4 border-t border-gray-700">
        <Link to={`/servers/${server.id}`} className="btn btn-secondary w-full text-center text-sm">
          {t('servers.manageServer')}
        </Link>
      </div>
    </div>
  )
}

interface ServerForm {
  name: string
  protocol: Protocol
  implementation: Implementation
  endpoint: string
  domain: string
  token: string
  mas: boolean
  masEndpoint: string
  masClientId: string
  masClientSecret: string
}

const defaultEndpoint: Record<Implementation, string> = {
  prosody: 'http://127.0.0.1:5280',
  ejabberd: 'http://127.0.0.1:5280',
  synapse: 'http://127.0.0.1:8008',
  tuwunel: 'http://127.0.0.1:8008',
  'matrix-generic': 'http://127.0.0.1:8008',
}

function AddServerModal({ onClose, onSuccess }: { onClose: () => void; onSuccess: () => void }) {
  const { t } = useTranslation()
  const { register, handleSubmit, watch, setValue, getValues, formState: { errors, isSubmitting } } = useForm<ServerForm>({
    defaultValues: { protocol: 'xmpp', implementation: 'prosody', endpoint: defaultEndpoint.prosody },
  })
  const protocol = watch('protocol')
  const implementation = watch('implementation')
  const mas = watch('mas')
  const options = implementationsFor(protocol)
  if (!options.includes(implementation) && options.length > 0) setValue('implementation', options[0])
  // The endpoint follows the implementation until the operator types their own.
  useEffect(() => {
    if (Object.values(defaultEndpoint).includes(getValues('endpoint'))) setValue('endpoint', defaultEndpoint[implementation])
  }, [implementation, getValues, setValue])

  const onSubmit = async (form: ServerForm) => {
    const body: CreateServerRequest = {
      name: form.name,
      protocol: form.protocol,
      implementation: form.implementation,
      endpoint: form.endpoint,
      domain: form.domain,
      credentials: form.implementation === 'synapse' && form.mas
        ? { kind: 'bearer+mas', token: form.token, mas: { endpoint: form.masEndpoint, client_id: form.masClientId, client_secret: form.masClientSecret } }
        : { kind: 'bearer', token: form.token },
    }
    try {
      await serversApi.create(body)
      toast.success(t('servers.addSuccess'))
      onSuccess()
    } catch (error) {
      toast.error(errorMessage(error) ?? t('servers.addFailed'))
    }
  }

  return (
    <Modal title={t('servers.addServerTitle')} onClose={onClose} width="max-w-lg">
      <form onSubmit={handleSubmit(onSubmit)} className="space-y-4">
        <Field label={t('common.name')} error={errors.name?.message}>
          <input type="text" className="input" placeholder={t('servers.namePlaceholder')} autoFocus {...register('name', { required: t('validation.required') })} />
        </Field>
        <div className="grid grid-cols-2 gap-4">
          <Field label={t('servers.protocol')}>
            <select className="input" {...register('protocol')}>
              {protocols.map((p) => <option key={p} value={p}>{t(`servers.protocols.${p}`)}</option>)}
            </select>
          </Field>
          <Field label={t('servers.implementation')}>
            <select className="input" {...register('implementation')}>
              {options.map((impl) => <option key={impl} value={impl}>{t(`servers.implementations.${impl}`)}</option>)}
            </select>
          </Field>
        </div>
        <Field label={t('servers.endpoint')} error={errors.endpoint?.message}>
          <input
            type="url"
            className="input font-mono text-sm"
            placeholder={defaultEndpoint[implementation]}
            {...register('endpoint', { required: t('validation.required'), pattern: { value: /^https?:\/\/\S+$/, message: t('servers.endpointHint') } })}
          />
          <p className="mt-1 text-xs text-gray-500">{t(`servers.endpointHelp.${implementation}`)}</p>
        </Field>
        <Field label={t(`servers.domainLabel.${protocol}`)} error={errors.domain?.message}>
          <input type="text" className="input" placeholder="example.com" {...register('domain', { required: t('validation.required') })} />
          <p className="mt-1 text-xs text-gray-500">{t(`servers.domainHelp.${protocol}`)}</p>
        </Field>
        <Field label={t('servers.token')} error={errors.token?.message}>
          <input type="password" className="input" placeholder={t('servers.tokenPlaceholder')} {...register('token', { required: t('validation.required') })} />
          <p className="mt-1 text-xs text-gray-500">{t(`servers.tokenHelp.${implementation}`)}</p>
        </Field>
        {implementation === 'synapse' && (
          <div className="space-y-4 rounded-lg border border-gray-700 p-4">
            <label className="flex items-center gap-2 text-sm text-gray-300">
              <input type="checkbox" {...register('mas')} />
              {t('servers.mas.toggle')}
            </label>
            {mas && (
              <>
                <Field label={t('servers.mas.endpoint')} error={errors.masEndpoint?.message}>
                  <input
                    type="url"
                    className="input font-mono text-sm"
                    placeholder="http://127.0.0.1:8080"
                    {...register('masEndpoint', { required: t('validation.required'), pattern: { value: /^https?:\/\/\S+$/, message: t('servers.endpointHint') } })}
                  />
                </Field>
                <Field label={t('servers.mas.clientId')} error={errors.masClientId?.message}>
                  <input type="text" className="input font-mono text-sm" {...register('masClientId', { required: t('validation.required') })} />
                </Field>
                <Field label={t('servers.mas.clientSecret')} error={errors.masClientSecret?.message}>
                  <input type="password" className="input" {...register('masClientSecret', { required: t('validation.required') })} />
                </Field>
                <p className="text-xs text-gray-500">{t('servers.mas.help')}</p>
              </>
            )}
          </div>
        )}
        <div className="flex justify-end gap-3 pt-4 border-t border-gray-700">
          <button type="button" onClick={onClose} className="btn btn-secondary">{t('common.cancel')}</button>
          <button type="submit" disabled={isSubmitting} className="btn btn-primary">{isSubmitting ? '...' : t('servers.addServer')}</button>
        </div>
      </form>
    </Modal>
  )
}
