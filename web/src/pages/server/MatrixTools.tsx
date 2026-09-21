import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation, Trans } from 'react-i18next'
import toast from 'react-hot-toast'
import { Copy, Plus, Send, Trash2 } from 'lucide-react'
import clsx from 'clsx'
import { useForm } from 'react-hook-form'
import {
  matrixApi, errorMessage, listErrorMessage,
  type Capability, type CreateRegistrationTokenRequest, type EventReport, type FederationDestination, type Page,
  type RegistrationToken, type Server, type ServerCapabilities,
} from '@/lib/api'
import PagedTable from '@/components/PagedTable'
import { usePaging } from '@/lib/paging'
import ConfirmDialog from '@/components/ConfirmDialog'
import Modal from '@/components/Modal'
import Field from '@/components/Field'

type ToolTab = 'tokens' | 'reports' | 'federation' | 'notices'

const toolCapability: Record<ToolTab, Capability> = {
  tokens: 'matrix.registration_tokens',
  reports: 'matrix.reports',
  federation: 'matrix.federation',
  notices: 'matrix.server_notice',
}

/** Tools tab: the server-wide Matrix moderation surfaces, one sub-tab per declared capability. */
export default function MatrixTools({ server, caps }: { server: Server; caps: ServerCapabilities }) {
  const { t } = useTranslation()
  const tabs = (Object.keys(toolCapability) as ToolTab[]).filter((tab) => caps.capabilities.includes(toolCapability[tab]))
  const [active, setActive] = useState<ToolTab>(tabs[0] ?? 'tokens')
  const current = tabs.includes(active) ? active : tabs[0]
  if (!current) return <p className="text-center text-gray-400 py-8">{t('backend.noCapabilities')}</p>

  return (
    <div>
      <div className="flex gap-2 mb-4 border-b border-gray-700">
        {tabs.map((tab) => (
          <button
            key={tab}
            onClick={() => setActive(tab)}
            className={clsx(
              'px-3 py-2 text-sm border-b-2 transition-colors',
              current === tab ? 'border-primary-500 text-primary-400' : 'border-transparent text-gray-400 hover:text-white'
            )}
          >
            {t(`backend.matrix.tabs.${tab}`)}
          </button>
        ))}
      </div>
      {current === 'tokens' && <RegistrationTokens server={server} />}
      {current === 'reports' && <Reports server={server} />}
      {current === 'federation' && <Federation server={server} />}
      {current === 'notices' && <Notices server={server} />}
    </div>
  )
}

function RegistrationTokens({ server }: { server: Server }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [showCreate, setShowCreate] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<RegistrationToken | null>(null)
  const key = ['matrix-tokens', server.id]
  const query = useQuery({
    queryKey: key,
    queryFn: async () => (await matrixApi.listRegistrationTokens(server.id)).data as RegistrationToken[],
  })
  const invalidate = () => queryClient.invalidateQueries({ queryKey: key })

  const remove = useMutation({
    mutationFn: (token: RegistrationToken) => matrixApi.deleteRegistrationToken(server.id, token.token),
    onSuccess: () => {
      toast.success(t('backend.matrix.tokens.deleted'))
      invalidate()
    },
    onError: (error) => toast.error(errorMessage(error) ?? t('backend.matrix.tokens.deleteFailed')),
    onSettled: () => setDeleteTarget(null),
  })

  const copy = async (token: string) => {
    try {
      await navigator.clipboard.writeText(token)
      toast.success(t('backend.matrix.tokens.copied'))
    } catch {
      toast.error(t('errors.generic'))
    }
  }

  const tokens = query.data ?? []
  return (
    <div>
      <div className="flex justify-end mb-4">
        <button onClick={() => setShowCreate(true)} className="btn btn-primary flex items-center gap-2">
          <Plus className="w-4 h-4" />
          {t('backend.matrix.tokens.create')}
        </button>
      </div>
      {query.isError ? (
        <p className="text-center text-red-400 py-8" role="alert">{listErrorMessage(query.error, t)}</p>
      ) : query.isLoading ? (
        <p className="text-center text-gray-400 py-8">{t('common.loading')}</p>
      ) : tokens.length === 0 ? (
        <p className="text-center text-gray-400 py-8">{t('backend.matrix.tokens.empty')}</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="text-left text-gray-400 border-b border-gray-700">
                <th className="py-2 pr-4">{t('backend.matrix.tokens.token')}</th>
                <th className="py-2 pr-4">{t('backend.matrix.tokens.uses')}</th>
                <th className="py-2 pr-4">{t('backend.matrix.tokens.expires')}</th>
                <th className="py-2 pr-4">{t('backend.matrix.tokens.status')}</th>
                <th className="py-2 w-24">{t('common.actions')}</th>
              </tr>
            </thead>
            <tbody>
              {tokens.map((token) => (
                <tr key={token.token} className="border-b border-gray-800">
                  <td className="py-2 pr-4 font-mono text-gray-200">{token.token}</td>
                  <td className="py-2 pr-4 text-gray-300">
                    {token.used} / {token.uses_allowed === null ? t('backend.matrix.tokens.unlimited') : token.uses_allowed}
                    {token.pending > 0 && <span className="text-gray-500"> (+{token.pending} {t('backend.matrix.tokens.pending')})</span>}
                  </td>
                  <td className="py-2 pr-4 text-gray-300">{token.expires_at ? new Date(token.expires_at).toLocaleString() : t('backend.matrix.tokens.never')}</td>
                  <td className="py-2 pr-4">
                    <span className={clsx('badge', token.revoked ? 'badge-gray' : token.valid ? 'badge-green' : 'badge-yellow')}>
                      {token.revoked ? t('backend.matrix.tokens.revoked') : token.valid ? t('backend.matrix.tokens.valid') : t('backend.matrix.tokens.invalid')}
                    </span>
                  </td>
                  <td className="py-2">
                    <div className="flex gap-1">
                      <button onClick={() => copy(token.token)} className="p-1 text-blue-400 hover:text-blue-300" title={t('backend.matrix.tokens.copy')} aria-label={t('backend.matrix.tokens.copy')}>
                        <Copy className="w-4 h-4" />
                      </button>
                      {!token.revoked && (
                        <button onClick={() => setDeleteTarget(token)} className="p-1 text-red-400 hover:text-red-300" title={t('common.delete')} aria-label={t('common.delete')}>
                          <Trash2 className="w-4 h-4" />
                        </button>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {showCreate && <CreateTokenModal server={server} onClose={() => setShowCreate(false)} onCreated={() => { setShowCreate(false); invalidate() }} />}
      <ConfirmDialog
        open={deleteTarget !== null}
        title={t('backend.matrix.tokens.deleteTitle')}
        message={<Trans i18nKey="backend.matrix.tokens.deletePrompt" values={{ token: deleteTarget?.token ?? '' }} components={{ strong: <span className="font-mono font-semibold text-white" /> }} />}
        confirmLabel={t('common.delete')}
        cancelLabel={t('common.cancel')}
        variant="danger"
        loading={remove.isPending}
        onConfirm={() => deleteTarget && remove.mutate(deleteTarget)}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  )
}

interface TokenForm {
  token: string
  uses_allowed: string
  expires_at: string
}

function CreateTokenModal({ server, onClose, onCreated }: { server: Server; onClose: () => void; onCreated: () => void }) {
  const { t } = useTranslation()
  const { register, handleSubmit, formState: { errors, isSubmitting } } = useForm<TokenForm>({ defaultValues: { token: '', uses_allowed: '', expires_at: '' } })
  const onSubmit = async (form: TokenForm) => {
    const body: CreateRegistrationTokenRequest = {}
    if (form.token.trim()) body.token = form.token.trim()
    if (form.uses_allowed.trim()) body.uses_allowed = parseInt(form.uses_allowed, 10)
    if (form.expires_at) body.expires_at = new Date(form.expires_at).toISOString()
    try {
      await matrixApi.createRegistrationToken(server.id, body)
      toast.success(t('backend.matrix.tokens.created'))
      onCreated()
    } catch (error) {
      toast.error(errorMessage(error) ?? t('backend.matrix.tokens.createFailed'))
    }
  }
  return (
    <Modal title={t('backend.matrix.tokens.createTitle')} onClose={onClose}>
      <form onSubmit={handleSubmit(onSubmit)} className="space-y-4">
        <Field label={t('backend.matrix.tokens.customToken')} error={errors.token?.message}>
          <input type="text" className="input font-mono text-sm" autoFocus {...register('token', { pattern: { value: /^[A-Za-z0-9._~-]{0,64}$/, message: t('backend.matrix.tokens.customTokenHelp') } })} />
          <p className="mt-1 text-xs text-gray-500">{t('backend.matrix.tokens.customTokenHelp')}</p>
        </Field>
        <Field label={t('backend.matrix.tokens.usesAllowed')} error={errors.uses_allowed?.message}>
          <input type="number" min={1} className="input" {...register('uses_allowed', { pattern: { value: /^([1-9]\d*)?$/, message: t('backend.matrix.tokens.usesAllowedHint') } })} />
        </Field>
        <Field label={t('backend.matrix.tokens.expiresAt')}>
          <input type="datetime-local" className="input" {...register('expires_at')} />
        </Field>
        <div className="flex justify-end gap-3 pt-2">
          <button type="button" onClick={onClose} className="btn btn-secondary">{t('common.cancel')}</button>
          <button type="submit" disabled={isSubmitting} className="btn btn-primary">{t('common.create')}</button>
        </div>
      </form>
    </Modal>
  )
}

function Reports({ server }: { server: Server }) {
  const { t } = useTranslation()
  const paging = usePaging()
  const query = useQuery({
    queryKey: ['matrix-reports', server.id, paging.params],
    queryFn: async () => (await matrixApi.listReports(server.id, paging.params)).data as Page<EventReport>,
    placeholderData: (previous) => previous,
  })
  return (
    <PagedTable
      columns={[
        { key: 'received', header: t('backend.matrix.reports.received'), render: (r: EventReport) => new Date(r.received_at).toLocaleString() },
        {
          key: 'room',
          header: t('backend.matrix.reports.room'),
          render: (r: EventReport) => (
            <div>
              <span className="text-white">{r.room_name || r.room_alias || r.room_id}</span>
              {(r.room_name || r.room_alias) && <span className="block font-mono text-xs text-gray-400">{r.room_id}</span>}
              <span className="block font-mono text-xs text-gray-500">{r.event_id}</span>
            </div>
          ),
        },
        { key: 'sender', header: t('backend.matrix.reports.sender'), render: (r: EventReport) => <span className="font-mono text-sm">{r.sender}</span> },
        { key: 'reporter', header: t('backend.matrix.reports.reporter'), render: (r: EventReport) => <span className="font-mono text-sm">{r.reporter}</span> },
        { key: 'reason', header: t('backend.matrix.reports.reason'), render: (r: EventReport) => r.reason || '—' },
        { key: 'score', header: t('backend.matrix.reports.score'), render: (r: EventReport) => r.score },
      ]}
      page={query.data}
      loading={query.isFetching}
      error={query.isError ? listErrorMessage(query.error, t) : undefined}
      rowKey={(r) => r.id}
      paging={paging}
      searchPlaceholder={t('backend.matrix.reports.searchPlaceholder')}
      emptyMessage={t('backend.matrix.reports.empty')}
    />
  )
}

function Federation({ server }: { server: Server }) {
  const { t } = useTranslation()
  const paging = usePaging()
  const query = useQuery({
    queryKey: ['matrix-federation', server.id, paging.params],
    queryFn: async () => (await matrixApi.listFederation(server.id, paging.params)).data as Page<FederationDestination>,
    placeholderData: (previous) => previous,
    refetchInterval: 30000,
  })
  return (
    <PagedTable
      columns={[
        { key: 'destination', header: t('backend.matrix.federation.destination'), render: (d: FederationDestination) => <span className="font-mono text-sm text-white">{d.destination}</span> },
        {
          key: 'status',
          header: t('backend.matrix.federation.status'),
          render: (d: FederationDestination) => (
            <span className={clsx('badge', d.healthy ? 'badge-green' : 'badge-yellow')}>
              {d.healthy ? t('backend.matrix.federation.healthy') : t('backend.matrix.federation.failing')}
            </span>
          ),
        },
        { key: 'since', header: t('backend.matrix.federation.failingSince'), render: (d: FederationDestination) => (d.failing_since ? new Date(d.failing_since).toLocaleString() : '—') },
        { key: 'last', header: t('backend.matrix.federation.lastFailure'), render: (d: FederationDestination) => (d.last_failure_at ? new Date(d.last_failure_at).toLocaleString() : '—') },
        { key: 'retry', header: t('backend.matrix.federation.retryIn'), render: (d: FederationDestination) => (d.retry_interval_ms > 0 ? `${Math.round(d.retry_interval_ms / 1000)}s` : '—') },
      ]}
      page={query.data}
      loading={query.isFetching}
      error={query.isError ? listErrorMessage(query.error, t) : undefined}
      rowKey={(d) => d.destination}
      paging={paging}
      searchPlaceholder={t('backend.matrix.federation.searchPlaceholder')}
      emptyMessage={t('backend.matrix.federation.empty')}
    />
  )
}

function Notices({ server }: { server: Server }) {
  const { t } = useTranslation()
  const { register, handleSubmit, reset, formState: { errors, isSubmitting } } = useForm<{ account: string; body: string }>()
  const onSubmit = async ({ account, body }: { account: string; body: string }) => {
    try {
      await matrixApi.sendNotice(server.id, account.trim(), body)
      toast.success(t('backend.matrix.notices.sent'))
      reset({ account: account.trim(), body: '' })
    } catch (error) {
      toast.error(errorMessage(error) ?? t('backend.matrix.notices.sendFailed'))
    }
  }
  return (
    <form onSubmit={handleSubmit(onSubmit)} className="space-y-4 max-w-xl">
      <p className="text-sm text-gray-400">{t('backend.matrix.notices.help')}</p>
      <Field label={t('backend.matrix.notices.account')} error={errors.account?.message}>
        <input type="text" className="input font-mono text-sm" placeholder={`@alice:${server.domain}`} {...register('account', { required: t('validation.required') })} />
        <p className="mt-1 text-xs text-gray-500">{t('backend.matrix.notices.accountHelp', { domain: server.domain })}</p>
      </Field>
      <Field label={t('backend.matrix.notices.body')} error={errors.body?.message}>
        <textarea className="input h-32 resize-y" {...register('body', { required: t('validation.required') })} />
      </Field>
      <div className="flex justify-end">
        <button type="submit" disabled={isSubmitting} className="btn btn-primary flex items-center gap-2">
          <Send className="w-4 h-4" />
          {t('backend.matrix.notices.send')}
        </button>
      </div>
    </form>
  )
}
