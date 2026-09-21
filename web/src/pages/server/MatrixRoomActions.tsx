import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { useTranslation, Trans } from 'react-i18next'
import toast from 'react-hot-toast'
import { Ban, Flame } from 'lucide-react'
import { matrixApi, errorMessage, type Capability, type Room, type Server, type ServerCapabilities } from '@/lib/api'
import ConfirmIdDialog from '@/components/ConfirmIdDialog'

interface Props {
  server: Server
  caps: ServerCapabilities
  room: Room
  onChanged: () => void
}

/** Per-room moderation buttons: block (a toggle, undoable) and purge (typed-id confirmation). */
export default function MatrixRoomActions({ server, caps, room, onChanged }: Props) {
  const { t } = useTranslation()
  const has = (c: Capability) => caps.capabilities.includes(c)
  const [purgeTarget, setPurgeTarget] = useState(false)
  const [blockToo, setBlockToo] = useState(true)
  const [newRoomUser, setNewRoomUser] = useState('')
  const [message, setMessage] = useState('')

  const block = useMutation({
    mutationFn: (value: boolean) => matrixApi.blockRoom(server.id, room.id, value),
    onSuccess: (_, value) => {
      toast.success(value ? t('backend.rooms.blocked') : t('backend.rooms.unblocked'))
      onChanged()
    },
    onError: (error) => toast.error(errorMessage(error) ?? t('backend.rooms.blockFailed')),
  })
  const purge = useMutation({
    mutationFn: () => matrixApi.purgeRoom(server.id, room.id, {
      purge: true, block: blockToo, force_purge: false,
      new_room_user: newRoomUser.trim() || undefined, message: newRoomUser.trim() ? message || undefined : undefined,
    }),
    onSuccess: (response) => {
      toast.success(t('backend.rooms.purgeStarted', { id: response.data.delete_id }))
      onChanged()
    },
    onError: (error) => toast.error(errorMessage(error) ?? t('backend.rooms.purgeFailed')),
    onSettled: () => setPurgeTarget(false),
  })

  return (
    <>
      {has('matrix.room_block') && (
        <>
          <button onClick={() => block.mutate(true)} className="p-1 text-yellow-400 hover:text-yellow-300" title={t('backend.rooms.block')} aria-label={t('backend.rooms.block')}>
            <Ban className="w-4 h-4" />
          </button>
          <button onClick={() => block.mutate(false)} className="p-1 text-gray-400 hover:text-white text-xs" title={t('backend.rooms.unblock')} aria-label={t('backend.rooms.unblock')}>
            {t('backend.rooms.unblock')}
          </button>
        </>
      )}
      {has('matrix.room_purge') && (
        <button onClick={() => setPurgeTarget(true)} className="p-1 text-red-400 hover:text-red-300" title={t('backend.rooms.purge')} aria-label={t('backend.rooms.purge')}>
          <Flame className="w-4 h-4" />
        </button>
      )}
      <ConfirmIdDialog
        open={purgeTarget}
        title={t('backend.rooms.purgeTitle')}
        message={<Trans i18nKey="backend.rooms.purgePrompt" values={{ id: room.id }} components={{ strong: <span className="font-mono font-semibold text-white" /> }} />}
        expectedId={room.id}
        idLabel={t('backend.rooms.confirmId')}
        mismatchHint={t('backend.rooms.confirmMismatch')}
        confirmLabel={t('backend.rooms.purge')}
        cancelLabel={t('common.cancel')}
        loading={purge.isPending}
        onConfirm={() => purge.mutate()}
        onCancel={() => setPurgeTarget(false)}
      >
        <div className="space-y-3 text-sm text-gray-300">
          <label className="flex items-center gap-2">
            <input type="checkbox" checked={blockToo} onChange={(e) => setBlockToo(e.target.checked)} />
            {t('backend.rooms.purgeBlock')}
          </label>
          <label className="block">
            {t('backend.rooms.purgeNewRoom')}
            <input type="text" className="input font-mono text-sm mt-1" placeholder={`@admin:${server.domain}`} value={newRoomUser} onChange={(e) => setNewRoomUser(e.target.value)} />
          </label>
          {newRoomUser.trim() && (
            <label className="block">
              {t('backend.rooms.purgeMessage')}
              <input type="text" className="input mt-1" value={message} onChange={(e) => setMessage(e.target.value)} />
            </label>
          )}
        </div>
      </ConfirmIdDialog>
    </>
  )
}
