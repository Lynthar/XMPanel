import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation, Trans } from 'react-i18next'
import toast from 'react-hot-toast'
import { Plus, Trash2 } from 'lucide-react'
import clsx from 'clsx'
import { useForm } from 'react-hook-form'
import {
  backendApi, errorMessage, listErrorMessage,
  type CreateRoomRequest, type Page, type Room, type Server, type ServerCapabilities,
} from '@/lib/api'
import PagedTable from '@/components/PagedTable'
import { usePaging } from '@/lib/paging'
import ConfirmDialog from '@/components/ConfirmDialog'
import Modal from '@/components/Modal'
import Field from '@/components/Field'
import MatrixRoomActions from './MatrixRoomActions'

/** Rooms tab: paged room listing with create and delete where the backend allows them. */
export default function Rooms({ server, caps }: { server: Server; caps: ServerCapabilities }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const paging = usePaging()
  const [showCreate, setShowCreate] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<Room | null>(null)
  const query = useQuery({
    queryKey: ['rooms', server.id, paging.params],
    queryFn: async () => (await backendApi.listRooms(server.id, paging.params)).data as Page<Room>,
    placeholderData: (previous) => previous,
  })
  const invalidate = () => queryClient.invalidateQueries({ queryKey: ['rooms', server.id] })
  const canDelete = caps.capabilities.includes('rooms.delete')
  const matrix = server.protocol === 'matrix' && (caps.capabilities.includes('matrix.room_block') || caps.capabilities.includes('matrix.room_purge'))

  const yesNo = (value: boolean, on = 'badge-green') => (
    <span className={clsx('badge', value ? on : 'badge-gray')}>{value ? t('common.yes') : t('common.no')}</span>
  )

  const remove = async (room: Room) => {
    setDeleteTarget(null)
    try {
      await backendApi.deleteRoom(server.id, room.id)
      toast.success(t('backend.rooms.deleted'))
      invalidate()
    } catch (error) {
      toast.error(errorMessage(error) ?? t('backend.rooms.deleteFailed'))
    }
  }

  return (
    <div>
      <PagedTable
        columns={[
          {
            key: 'room',
            header: t('backend.rooms.room'),
            render: (r: Room) => (
              <div>
                <span className="text-white">{r.name || r.id}</span>
                {r.name && r.name !== r.id && <span className="block font-mono text-xs text-gray-400">{r.id}</span>}
                {r.alias && <span className="block font-mono text-xs text-gray-400">{r.alias}</span>}
              </div>
            ),
          },
          { key: 'members', header: t('backend.rooms.members'), render: (r: Room) => r.members },
          { key: 'public', header: t('backend.rooms.public'), render: (r: Room) => yesNo(r.public) },
          { key: 'persistent', header: t('backend.rooms.persistent'), render: (r: Room) => (r.xmpp ? yesNo(r.xmpp.persistent, 'badge-blue') : '—') },
          { key: 'membersOnly', header: t('backend.rooms.membersOnly'), render: (r: Room) => (r.xmpp ? yesNo(r.xmpp.members_only, 'badge-yellow') : '—') },
          ...(canDelete || matrix
            ? [{
                key: 'actions',
                header: t('common.actions'),
                className: matrix ? 'w-44' : 'w-20',
                render: (r: Room) => (
                  <div className="flex items-center gap-1">
                    {canDelete && (
                      <button onClick={() => setDeleteTarget(r)} className="p-1 text-red-400 hover:text-red-300" title={t('common.delete')} aria-label={t('common.delete')}>
                        <Trash2 className="w-4 h-4" />
                      </button>
                    )}
                    {matrix && <MatrixRoomActions server={server} caps={caps} room={r} onChanged={invalidate} />}
                  </div>
                ),
              }]
            : []),
        ]}
        page={query.data}
        loading={query.isFetching}
        error={query.isError ? listErrorMessage(query.error, t) : undefined}
        rowKey={(r) => r.id}
        paging={paging}
        searchPlaceholder={t('backend.rooms.searchPlaceholder')}
        emptyMessage={t('backend.rooms.empty')}
        toolbar={
          caps.capabilities.includes('rooms.create') && (
            <button onClick={() => setShowCreate(true)} className="btn btn-primary flex items-center gap-2">
              <Plus className="w-4 h-4" />
              {t('backend.rooms.create')}
            </button>
          )
        }
      />
      {showCreate && (
        <CreateRoomModal server={server} onClose={() => setShowCreate(false)} onCreated={() => { setShowCreate(false); invalidate() }} />
      )}
      <ConfirmDialog
        open={deleteTarget !== null}
        title={t('backend.rooms.deleteTitle')}
        message={<Trans i18nKey="backend.rooms.deletePrompt" values={{ id: deleteTarget?.id ?? '' }} components={{ strong: <span className="font-semibold text-white" /> }} />}
        confirmLabel={t('common.delete')}
        cancelLabel={t('common.cancel')}
        variant="danger"
        onConfirm={() => deleteTarget && remove(deleteTarget)}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  )
}

function CreateRoomModal({ server, onClose, onCreated }: { server: Server; onClose: () => void; onCreated: () => void }) {
  const { t } = useTranslation()
  const { register, handleSubmit, formState: { errors, isSubmitting } } = useForm<CreateRoomRequest>({
    defaultValues: { domain: `conference.${server.domain}`, public: true, persistent: true, members_only: false },
  })
  const onSubmit = async (data: CreateRoomRequest) => {
    try {
      await backendApi.createRoom(server.id, data)
      toast.success(t('backend.rooms.created'))
      onCreated()
    } catch (error) {
      toast.error(errorMessage(error) ?? t('backend.rooms.createFailed'))
    }
  }
  return (
    <Modal title={t('backend.rooms.createTitle')} onClose={onClose}>
      <form onSubmit={handleSubmit(onSubmit)} className="space-y-4">
        <Field label={t('backend.rooms.name')} error={errors.name?.message}>
          <input type="text" className="input" autoFocus {...register('name', { required: t('validation.required') })} />
        </Field>
        <Field label={t('backend.rooms.service')} error={errors.domain?.message}>
          <input type="text" className="input" {...register('domain', { required: t('validation.required') })} />
        </Field>
        <Field label={t('common.description')}>
          <input type="text" className="input" {...register('description')} />
        </Field>
        <div className="flex gap-6 text-sm text-gray-300">
          <label className="flex items-center gap-2"><input type="checkbox" {...register('public')} /> {t('backend.rooms.public')}</label>
          <label className="flex items-center gap-2"><input type="checkbox" {...register('persistent')} /> {t('backend.rooms.persistent')}</label>
          <label className="flex items-center gap-2"><input type="checkbox" {...register('members_only')} /> {t('backend.rooms.membersOnly')}</label>
        </div>
        <div className="flex justify-end gap-3 pt-2">
          <button type="button" onClick={onClose} className="btn btn-secondary">{t('common.cancel')}</button>
          <button type="submit" disabled={isSubmitting} className="btn btn-primary">{t('common.create')}</button>
        </div>
      </form>
    </Modal>
  )
}
