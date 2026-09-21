import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation, Trans } from 'react-i18next'
import toast from 'react-hot-toast'
import { EyeOff, Image, PauseCircle, PlayCircle, ShieldAlert, Trash2, UserX } from 'lucide-react'
import clsx from 'clsx'
import {
  matrixApi, errorMessage, listErrorMessage,
  type Account, type Capability, type Media, type Page, type Server, type ServerCapabilities,
} from '@/lib/api'
import ConfirmDialog from '@/components/ConfirmDialog'
import ConfirmIdDialog from '@/components/ConfirmIdDialog'
import Modal from '@/components/Modal'
import PagedTable from '@/components/PagedTable'
import { usePaging } from '@/lib/paging'

interface Props {
  server: Server
  caps: ServerCapabilities
  account: Account
  onChanged: () => void
}

/** The Matrix-only states of an account, shown beside its id. */
export function MatrixAccountBadges({ account }: { account: Account }) {
  const { t } = useTranslation()
  const facts = account.matrix
  if (!facts) return null
  return (
    <>
      {facts.suspended && <span className="badge badge-yellow">{t('backend.accounts.suspended')}</span>}
      {facts.shadow_banned && <span className="badge badge-red">{t('backend.accounts.shadowBanned')}</span>}
      {facts.erased && <span className="badge badge-gray">{t('backend.accounts.erased')}</span>}
    </>
  )
}

/** Per-account moderation buttons for the capabilities the server declares. */
export default function MatrixAccountActions({ server, caps, account, onChanged }: Props) {
  const { t } = useTranslation()
  const has = (c: Capability) => caps.capabilities.includes(c)
  const [shadowBanTarget, setShadowBanTarget] = useState(false)
  const [eraseTarget, setEraseTarget] = useState(false)
  const [showMedia, setShowMedia] = useState(false)
  const banned = account.matrix?.shadow_banned ?? false

  const setSuspended = useMutation({
    mutationFn: (value: boolean) => matrixApi.setSuspended(server.id, account.id, value),
    onSuccess: () => {
      toast.success(t('backend.accounts.updated'))
      onChanged()
    },
    onError: (error) => toast.error(errorMessage(error) ?? t('backend.accounts.updateFailed')),
  })
  const setShadowBanned = useMutation({
    mutationFn: (value: boolean) => matrixApi.setShadowBanned(server.id, account.id, value),
    onSuccess: () => {
      toast.success(t('backend.accounts.updated'))
      onChanged()
    },
    onError: (error) => toast.error(errorMessage(error) ?? t('backend.accounts.updateFailed')),
    onSettled: () => setShadowBanTarget(false),
  })
  const erase = useMutation({
    mutationFn: () => matrixApi.deactivate(server.id, account.id, true),
    onSuccess: () => {
      toast.success(t('backend.accounts.deactivated'))
      onChanged()
    },
    onError: (error) => toast.error(errorMessage(error) ?? t('backend.accounts.updateFailed')),
    onSettled: () => setEraseTarget(false),
  })

  return (
    <>
      {/* The listing does not carry the suspended state (only the single-account
          query does), so both directions are offered instead of a toggle. */}
      {has('matrix.suspend') && (
        <>
          <button onClick={() => setSuspended.mutate(true)} className="p-1 text-yellow-400 hover:text-yellow-300" title={t('backend.accounts.suspend')} aria-label={t('backend.accounts.suspend')}>
            <PauseCircle className="w-4 h-4" />
          </button>
          <button onClick={() => setSuspended.mutate(false)} className="p-1 text-green-400 hover:text-green-300" title={t('backend.accounts.unsuspend')} aria-label={t('backend.accounts.unsuspend')}>
            <PlayCircle className="w-4 h-4" />
          </button>
        </>
      )}
      {has('matrix.shadow_ban') && (
        <button
          onClick={() => (banned ? setShadowBanned.mutate(false) : setShadowBanTarget(true))}
          className={clsx('p-1', banned ? 'text-green-400 hover:text-green-300' : 'text-red-400 hover:text-red-300')}
          title={banned ? t('backend.accounts.unshadowBan') : t('backend.accounts.shadowBan')}
          aria-label={banned ? t('backend.accounts.unshadowBan') : t('backend.accounts.shadowBan')}
        >
          <EyeOff className="w-4 h-4" />
        </button>
      )}
      {has('matrix.media') && (
        <button onClick={() => setShowMedia(true)} className="p-1 text-blue-400 hover:text-blue-300" title={t('backend.accounts.media')} aria-label={t('backend.accounts.media')}>
          <Image className="w-4 h-4" />
        </button>
      )}
      {has('matrix.deactivate') && (
        <button onClick={() => setEraseTarget(true)} className="p-1 text-red-400 hover:text-red-300" title={t('backend.accounts.deactivate')} aria-label={t('backend.accounts.deactivate')}>
          <UserX className="w-4 h-4" />
        </button>
      )}

      <ConfirmIdDialog
        open={shadowBanTarget}
        title={t('backend.accounts.shadowBanTitle')}
        message={<Trans i18nKey="backend.accounts.shadowBanPrompt" values={{ id: account.id }} components={{ strong: <span className="font-mono font-semibold text-white" /> }} />}
        expectedId={account.id}
        idLabel={t('backend.accounts.confirmId')}
        mismatchHint={t('backend.accounts.confirmMismatch')}
        confirmLabel={t('backend.accounts.shadowBan')}
        cancelLabel={t('common.cancel')}
        loading={setShadowBanned.isPending}
        onConfirm={() => setShadowBanned.mutate(true)}
        onCancel={() => setShadowBanTarget(false)}
      />
      <ConfirmIdDialog
        open={eraseTarget}
        title={t('backend.accounts.deactivateTitle')}
        message={<Trans i18nKey="backend.accounts.deactivatePrompt" values={{ id: account.id }} components={{ strong: <span className="font-mono font-semibold text-white" /> }} />}
        expectedId={account.id}
        idLabel={t('backend.accounts.confirmId')}
        mismatchHint={t('backend.accounts.confirmMismatch')}
        confirmLabel={t('backend.accounts.deactivate')}
        cancelLabel={t('common.cancel')}
        loading={erase.isPending}
        onConfirm={() => erase.mutate()}
        onCancel={() => setEraseTarget(false)}
      />
      {showMedia && <MediaModal server={server} account={account} onClose={() => setShowMedia(false)} />}
    </>
  )
}

function MediaModal({ server, account, onClose }: { server: Server; account: Account; onClose: () => void }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const paging = usePaging(20)
  const [deleteTarget, setDeleteTarget] = useState<Media | null>(null)
  const key = ['matrix-media', server.id, account.id]
  const query = useQuery({
    queryKey: [...key, paging.params],
    queryFn: async () => (await matrixApi.listAccountMedia(server.id, account.id, paging.params)).data as Page<Media>,
    placeholderData: (previous) => previous,
  })
  const invalidate = () => queryClient.invalidateQueries({ queryKey: key })
  const quarantine = useMutation({
    mutationFn: () => matrixApi.quarantineAccountMedia(server.id, account.id),
    onSuccess: (response) => {
      toast.success(t('backend.accounts.quarantined', { count: response.data.quarantined ?? 0 }))
      invalidate()
    },
    onError: (error) => toast.error(errorMessage(error) ?? t('backend.accounts.updateFailed')),
  })
  const remove = useMutation({
    mutationFn: (media: Media) => matrixApi.deleteMedia(server.id, media.id),
    onSuccess: () => {
      toast.success(t('backend.accounts.mediaDeleted'))
      invalidate()
    },
    onError: (error) => toast.error(errorMessage(error) ?? t('backend.accounts.updateFailed')),
    onSettled: () => setDeleteTarget(null),
  })
  const size = (bytes: number) => (bytes >= 1048576 ? `${(bytes / 1048576).toFixed(1)} MB` : bytes >= 1024 ? `${(bytes / 1024).toFixed(1)} KB` : `${bytes} B`)

  return (
    <Modal title={t('backend.accounts.mediaTitle', { id: account.id })} onClose={onClose} width="max-w-3xl">
      <PagedTable
        columns={[
          {
            key: 'name',
            header: t('backend.accounts.mediaName'),
            render: (m: Media) => (
              <div>
                <span className="text-white">{m.name || m.id}</span>
                {m.name && <span className="block font-mono text-xs text-gray-400">{m.id}</span>}
                {m.quarantined && <span className="badge badge-yellow ml-2">{t('backend.accounts.mediaQuarantined')}</span>}
              </div>
            ),
          },
          { key: 'type', header: t('backend.accounts.mediaType'), render: (m: Media) => m.type || '—' },
          { key: 'size', header: t('backend.accounts.mediaSize'), render: (m: Media) => size(m.size) },
          { key: 'uploaded', header: t('backend.accounts.mediaUploaded'), render: (m: Media) => (m.created_at ? new Date(m.created_at).toLocaleString() : '—') },
          {
            key: 'actions',
            header: t('common.actions'),
            className: 'w-16',
            render: (m: Media) => (
              <button onClick={() => setDeleteTarget(m)} className="p-1 text-red-400 hover:text-red-300" title={t('common.delete')} aria-label={t('common.delete')}>
                <Trash2 className="w-4 h-4" />
              </button>
            ),
          },
        ]}
        page={query.data}
        loading={query.isFetching}
        error={query.isError ? listErrorMessage(query.error, t) : undefined}
        rowKey={(m) => m.id}
        paging={paging}
        searchPlaceholder={t('backend.accounts.mediaSearchPlaceholder')}
        emptyMessage={t('backend.accounts.mediaEmpty')}
        toolbar={
          <button onClick={() => quarantine.mutate()} disabled={quarantine.isPending} className="btn btn-secondary flex items-center gap-2 text-yellow-400">
            <ShieldAlert className="w-4 h-4" />
            {t('backend.accounts.quarantineAll')}
          </button>
        }
      />
      <ConfirmDialog
        open={deleteTarget !== null}
        title={t('backend.accounts.mediaDeleteTitle')}
        message={<Trans i18nKey="backend.accounts.mediaDeletePrompt" values={{ id: deleteTarget?.id ?? '' }} components={{ strong: <span className="font-mono font-semibold text-white" /> }} />}
        confirmLabel={t('common.delete')}
        cancelLabel={t('common.cancel')}
        variant="danger"
        loading={remove.isPending}
        onConfirm={() => deleteTarget && remove.mutate(deleteTarget)}
        onCancel={() => setDeleteTarget(null)}
      />
    </Modal>
  )
}
