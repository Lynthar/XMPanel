import { useEffect, useState, type ReactNode } from 'react'
import { AlertTriangle } from 'lucide-react'
import Modal from '@/components/Modal'

interface ConfirmIdDialogProps {
  open: boolean
  title: string
  message: ReactNode
  /** The id the operator must type in full before the action is enabled. */
  expectedId: string
  idLabel: string
  mismatchHint: string
  confirmLabel: string
  cancelLabel: string
  loading?: boolean
  /** Extra fields rendered between the warning and the id box. */
  children?: ReactNode
  onConfirm: () => void
  onCancel: () => void
}

/**
 * ConfirmIdDialog guards an irreversible action: the confirm button stays
 * disabled until the target id is typed exactly, so a misclick cannot erase
 * or purge anything.
 */
export default function ConfirmIdDialog({
  open, title, message, expectedId, idLabel, mismatchHint, confirmLabel, cancelLabel, loading = false, children, onConfirm, onCancel,
}: ConfirmIdDialogProps) {
  const [typed, setTyped] = useState('')
  useEffect(() => {
    if (open) setTyped('')
  }, [open, expectedId])
  if (!open) return null
  const matches = typed === expectedId

  return (
    <Modal title={title} onClose={loading ? () => {} : onCancel}>
      <div className="space-y-4">
        <div className="flex items-start gap-3 text-red-300">
          <AlertTriangle className="w-5 h-5 text-red-400 mt-0.5 flex-shrink-0" />
          <div className="text-sm">{message}</div>
        </div>
        {children}
        <label className="block text-sm text-gray-300">
          {idLabel}
          <input
            type="text"
            className="input font-mono text-sm mt-1"
            value={typed}
            onChange={(e) => setTyped(e.target.value)}
            placeholder={expectedId}
            autoComplete="off"
            spellCheck={false}
            aria-invalid={typed !== '' && !matches}
          />
        </label>
        {typed !== '' && !matches && <p className="text-xs text-red-400">{mismatchHint}</p>}
        <div className="flex justify-end gap-3 pt-2 border-t border-gray-700">
          <button type="button" onClick={onCancel} disabled={loading} className="btn btn-secondary">{cancelLabel}</button>
          <button
            type="button"
            onClick={onConfirm}
            disabled={!matches || loading}
            className="btn bg-red-600 hover:bg-red-500 text-white disabled:opacity-50"
          >
            {loading ? `${confirmLabel}...` : confirmLabel}
          </button>
        </div>
      </div>
    </Modal>
  )
}
