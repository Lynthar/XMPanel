import { useEffect, useRef } from 'react'
import { AlertTriangle, X } from 'lucide-react'
import clsx from 'clsx'

interface ConfirmDialogProps {
  open: boolean
  title: string
  message: React.ReactNode
  confirmLabel?: string
  cancelLabel?: string
  variant?: 'danger' | 'primary'
  loading?: boolean
  onConfirm: () => void
  onCancel: () => void
}

export default function ConfirmDialog({
  open,
  title,
  message,
  confirmLabel = 'Confirm',
  cancelLabel = 'Cancel',
  variant = 'primary',
  loading = false,
  onConfirm,
  onCancel,
}: ConfirmDialogProps) {
  // Auto-focus the cancel button so destructive actions need an explicit click.
  const cancelRef = useRef<HTMLButtonElement>(null)

  useEffect(() => {
    if (!open) return
    cancelRef.current?.focus()

    const handleKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !loading) onCancel()
    }
    window.addEventListener('keydown', handleKey)
    return () => window.removeEventListener('keydown', handleKey)
  }, [open, loading, onCancel])

  if (!open) return null

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/50"
      role="dialog"
      aria-modal="true"
      aria-labelledby="confirm-dialog-title"
      onClick={(e) => {
        // Only dismiss when the backdrop itself is clicked, never on the panel.
        if (e.target === e.currentTarget && !loading) onCancel()
      }}
    >
      <div className="w-full max-w-md bg-gray-800 rounded-xl border border-gray-700 shadow-xl">
        <div className="flex items-center justify-between p-4 border-b border-gray-700">
          <h2 id="confirm-dialog-title" className="text-lg font-semibold text-white">
            {title}
          </h2>
          <button
            onClick={onCancel}
            disabled={loading}
            className="text-gray-400 hover:text-white disabled:opacity-50"
            aria-label="Close"
          >
            <X className="w-5 h-5" />
          </button>
        </div>

        <div className="p-4 space-y-3">
          {variant === 'danger' && (
            <div className="flex items-start gap-3 text-red-300">
              <AlertTriangle className="w-5 h-5 text-red-400 mt-0.5 flex-shrink-0" />
              <div className="text-sm">{message}</div>
            </div>
          )}
          {variant !== 'danger' && <div className="text-sm text-gray-300">{message}</div>}
        </div>

        <div className="flex justify-end gap-3 p-4 border-t border-gray-700">
          <button
            ref={cancelRef}
            onClick={onCancel}
            disabled={loading}
            className="btn btn-secondary"
          >
            {cancelLabel}
          </button>
          <button
            onClick={onConfirm}
            disabled={loading}
            className={clsx(
              'btn',
              variant === 'danger'
                ? 'bg-red-600 hover:bg-red-500 text-white disabled:opacity-50'
                : 'btn-primary'
            )}
          >
            {loading ? `${confirmLabel}...` : confirmLabel}
          </button>
        </div>
      </div>
    </div>
  )
}
