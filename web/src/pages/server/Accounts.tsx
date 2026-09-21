import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation, Trans } from 'react-i18next'
import toast from 'react-hot-toast'
import { ChevronDown, ChevronRight, KeyRound, LogOut, Power, Trash2, UserPlus, Upload } from 'lucide-react'
import clsx from 'clsx'
import { useForm } from 'react-hook-form'
import {
  backendApi, errorMessage, isNotFound, listErrorMessage,
  type Account, type Capability, type CreateAccountRequest, type Page, type Server, type ServerCapabilities, type Session,
} from '@/lib/api'
import PagedTable from '@/components/PagedTable'
import { usePaging } from '@/lib/paging'
import ConfirmDialog from '@/components/ConfirmDialog'
import Modal from '@/components/Modal'
import SessionRows from './SessionRows'
import MatrixAccountActions, { MatrixAccountBadges } from './MatrixAccountActions'
import Field from '@/components/Field'
import { parseAccountsCsv } from '@/lib/csv'

interface Props {
  server: Server
  caps: ServerCapabilities
}

/**
 * Accounts tab: one page of accounts with per-row actions the backend
 * declares. A backend that can only look accounts up (no listing) gets the
 * same table with the search box taking one id and showing that one row.
 */
export default function Accounts({ server, caps }: Props) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const has = (c: Capability) => caps.capabilities.includes(c)
  const lookupOnly = !has('accounts.list')
  const paging = usePaging()
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [showAdd, setShowAdd] = useState(false)
  const [showImport, setShowImport] = useState(false)
  const [passwordTarget, setPasswordTarget] = useState<Account | null>(null)
  const [deleteTargets, setDeleteTargets] = useState<Account[] | null>(null)

  const accountsKey = ['accounts', server.id]
  const query = useQuery({
    queryKey: [...accountsKey, paging.params],
    queryFn: async (): Promise<Page<Account>> => {
      if (!lookupOnly) return (await backendApi.listAccounts(server.id, paging.params)).data as Page<Account>
      const id = paging.params.search?.trim()
      if (!id) return { items: [] }
      try {
        return { items: [(await backendApi.getAccount(server.id, id)).data as Account] }
      } catch (error) {
        if (isNotFound(error)) return { items: [] }
        throw error
      }
    },
    placeholderData: (previous) => previous,
  })
  const invalidate = () => {
    queryClient.invalidateQueries({ queryKey: accountsKey })
    queryClient.invalidateQueries({ queryKey: ['server-stats', server.id] })
  }

  const setEnabled = useMutation({
    mutationFn: ({ account, enabled }: { account: Account; enabled: boolean }) =>
      backendApi.setEnabled(server.id, account.id, enabled),
    onSuccess: () => {
      toast.success(t('backend.accounts.updated'))
      invalidate()
    },
    onError: (error) => toast.error(errorMessage(error) ?? t('backend.accounts.updateFailed')),
  })

  const terminateAll = useMutation({
    mutationFn: (account: Account) => backendApi.terminateAccountSessions(server.id, account.id),
    onSuccess: () => {
      toast.success(t('backend.sessions.terminatedAll'))
      queryClient.invalidateQueries({ queryKey: ['sessions', server.id] })
      queryClient.invalidateQueries({ queryKey: ['account-sessions', server.id] })
    },
    onError: (error) => toast.error(errorMessage(error) ?? t('backend.sessions.terminateFailed')),
  })

  // Deletions run a few at a time; each failure is counted, not thrown, so
  // one bad row does not stop the batch.
  const runDelete = async (targets: Account[]) => {
    setDeleteTargets(null)
    let ok = 0
    let failed = 0
    let i = 0
    const worker = async () => {
      while (i < targets.length) {
        const target = targets[i++]
        try {
          await backendApi.deleteAccount(server.id, target.id)
          ok++
        } catch {
          failed++
        }
      }
    }
    await Promise.all(Array.from({ length: 4 }, worker))
    invalidate()
    setSelected(new Set())
    if (failed === 0) toast.success(t('backend.accounts.deleted', { count: ok }))
    else toast.error(t('backend.accounts.deletePartial', { ok, failed }))
  }

  const toggle = (set: Set<string>, key: string) => {
    const next = new Set(set)
    if (next.has(key)) next.delete(key)
    else next.add(key)
    return next
  }
  const items = query.data?.items ?? []
  const allSelected = items.length > 0 && items.every((a) => selected.has(a.id))

  const columns = [
    ...(has('accounts.delete')
      ? [{
          key: 'select',
          className: 'w-10',
          header: (
            <input
              type="checkbox"
              className="w-4 h-4 rounded bg-gray-700 border-gray-600 text-primary-600 focus:ring-primary-500"
              checked={allSelected}
              onChange={() => setSelected(allSelected ? new Set() : new Set(items.map((a) => a.id)))}
              aria-label={t('backend.accounts.selectAll')}
            />
          ),
          render: (a: Account) => (
            <input
              type="checkbox"
              className="w-4 h-4 rounded bg-gray-700 border-gray-600 text-primary-600 focus:ring-primary-500"
              checked={selected.has(a.id)}
              onChange={() => setSelected(toggle(selected, a.id))}
              aria-label={a.id}
            />
          ),
        }]
      : []),
    {
      key: 'id',
      header: t('backend.accounts.id'),
      render: (a: Account) => (
        <div className="flex items-center gap-2">
          {has('sessions.list_by_account') && (
            <button
              onClick={() => setExpanded(toggle(expanded, a.id))}
              className="text-gray-400 hover:text-white"
              aria-expanded={expanded.has(a.id)}
              aria-label={t(`backend.sessions.${server.protocol}.show`)}
            >
              {expanded.has(a.id) ? <ChevronDown className="w-4 h-4" /> : <ChevronRight className="w-4 h-4" />}
            </button>
          )}
          <span className="font-mono text-sm text-gray-200">{a.id}</span>
          {a.display_name && <span className="text-gray-400">{a.display_name}</span>}
          {a.admin && <span className="badge badge-blue">{t('backend.accounts.admin')}</span>}
          <MatrixAccountBadges account={a} />
        </div>
      ),
    },
    {
      key: 'status',
      header: t('common.status'),
      render: (a: Account) => (
        <span className={clsx('badge', a.enabled ? 'badge-green' : 'badge-gray')}>
          {a.enabled ? t('backend.accounts.enabled') : t('backend.accounts.disabled')}
        </span>
      ),
    },
    {
      key: 'actions',
      header: t('common.actions'),
      className: server.protocol === 'matrix' ? 'w-64' : 'w-40',
      render: (a: Account) => (
        <div className="flex gap-1">
          {has('accounts.set_password') && (
            <button onClick={() => setPasswordTarget(a)} className="p-1 text-blue-400 hover:text-blue-300" title={t('backend.accounts.setPassword')} aria-label={t('backend.accounts.setPassword')}>
              <KeyRound className="w-4 h-4" />
            </button>
          )}
          {has('accounts.set_enabled') && (
            <button
              onClick={() => setEnabled.mutate({ account: a, enabled: !a.enabled })}
              className={clsx('p-1', a.enabled ? 'text-yellow-400 hover:text-yellow-300' : 'text-green-400 hover:text-green-300')}
              title={a.enabled ? t('backend.accounts.disable') : t('backend.accounts.enable')}
              aria-label={a.enabled ? t('backend.accounts.disable') : t('backend.accounts.enable')}
            >
              <Power className="w-4 h-4" />
            </button>
          )}
          {has('sessions.terminate') && (
            <button onClick={() => terminateAll.mutate(a)} className="p-1 text-orange-400 hover:text-orange-300" title={t(`backend.sessions.${server.protocol}.terminateAll`)} aria-label={t(`backend.sessions.${server.protocol}.terminateAll`)}>
              <LogOut className="w-4 h-4" />
            </button>
          )}
          {has('accounts.delete') && (
            <button onClick={() => setDeleteTargets([a])} className="p-1 text-red-400 hover:text-red-300" title={t('common.delete')} aria-label={t('common.delete')}>
              <Trash2 className="w-4 h-4" />
            </button>
          )}
          {server.protocol === 'matrix' && <MatrixAccountActions server={server} caps={caps} account={a} onChanged={invalidate} />}
        </div>
      ),
    },
  ]

  return (
    <div>
      <PagedTable
        columns={columns}
        page={query.data}
        loading={query.isFetching}
        error={query.isError ? listErrorMessage(query.error, t) : undefined}
        rowKey={(a) => a.id}
        paging={paging}
        searchPlaceholder={t(lookupOnly ? 'backend.accounts.lookupPlaceholder' : 'backend.accounts.searchPlaceholder')}
        emptyMessage={t(lookupOnly ? (paging.params.search ? 'backend.accounts.lookupMissing' : 'backend.accounts.lookupHint') : 'backend.accounts.empty')}
        expandedKeys={expanded}
        expansion={(a) => <AccountSessions server={server} account={a} canTerminate={has('sessions.terminate')} />}
        toolbar={
          <>
            {selected.size > 0 && has('accounts.delete') && (
              <button
                onClick={() => setDeleteTargets(items.filter((a) => selected.has(a.id)))}
                className="btn btn-secondary flex items-center gap-2 text-red-400 hover:text-red-300"
              >
                <Trash2 className="w-4 h-4" />
                {t('backend.accounts.deleteSelected', { count: selected.size })}
              </button>
            )}
            {has('accounts.create') && (
              <>
                <button onClick={() => setShowImport(true)} className="btn btn-secondary flex items-center gap-2">
                  <Upload className="w-4 h-4" />
                  {t('backend.accounts.importCsv')}
                </button>
                <button onClick={() => setShowAdd(true)} className="btn btn-primary flex items-center gap-2">
                  <UserPlus className="w-4 h-4" />
                  {t('backend.accounts.add')}
                </button>
              </>
            )}
          </>
        }
      />

      {showAdd && (
        <AddAccountModal server={server} onClose={() => setShowAdd(false)} onCreated={() => { setShowAdd(false); invalidate() }} />
      )}
      {showImport && (
        <ImportCsvModal server={server} onClose={() => setShowImport(false)} onDone={() => { setShowImport(false); invalidate() }} />
      )}
      {passwordTarget && (
        <SetPasswordModal server={server} account={passwordTarget} onClose={() => setPasswordTarget(null)} />
      )}
      <ConfirmDialog
        open={deleteTargets !== null}
        title={t('backend.accounts.deleteTitle', { count: deleteTargets?.length ?? 0 })}
        message={
          deleteTargets?.length === 1 ? (
            <Trans i18nKey="backend.accounts.deletePrompt" values={{ id: deleteTargets[0].id }} components={{ strong: <span className="font-semibold text-white" /> }} />
          ) : (
            <Trans i18nKey="backend.accounts.deleteManyPrompt" values={{ count: deleteTargets?.length ?? 0 }} components={{ strong: <span className="font-semibold text-white" /> }} />
          )
        }
        confirmLabel={t('common.delete')}
        cancelLabel={t('common.cancel')}
        variant="danger"
        onConfirm={() => deleteTargets && runDelete(deleteTargets)}
        onCancel={() => setDeleteTargets(null)}
      />
    </div>
  )
}

/** The sessions of one account, fetched when its row is expanded. */
function AccountSessions({ server, account, canTerminate }: { server: Server; account: Account; canTerminate: boolean }) {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: ['account-sessions', server.id, account.id],
    queryFn: async () => (await backendApi.listAccountSessions(server.id, account.id)).data as Session[],
    refetchInterval: 10000,
  })
  if (query.isLoading) return <p className="text-sm text-gray-400">{t('common.loading')}</p>
  if (query.isError) return <p className="text-sm text-red-400">{errorMessage(query.error) ?? t('errors.generic')}</p>
  return <SessionRows server={server} sessions={query.data ?? []} canTerminate={canTerminate} />
}

function AddAccountModal({ server, onClose, onCreated }: { server: Server; onClose: () => void; onCreated: () => void }) {
  const { t } = useTranslation()
  const { register, handleSubmit, formState: { errors, isSubmitting } } = useForm<CreateAccountRequest>({
    defaultValues: { domain: server.domain },
  })
  const onSubmit = async (data: CreateAccountRequest) => {
    try {
      await backendApi.createAccount(server.id, data)
      toast.success(t('backend.accounts.created'))
      onCreated()
    } catch (error) {
      toast.error(errorMessage(error) ?? t('backend.accounts.createFailed'))
    }
  }
  return (
    <Modal title={t('backend.accounts.addTitle')} onClose={onClose}>
      <form onSubmit={handleSubmit(onSubmit)} className="space-y-4">
        <Field label={t('backend.accounts.localpart')} error={errors.localpart?.message}>
          <input type="text" className="input" autoFocus {...register('localpart', { required: t('validation.required') })} />
        </Field>
        <Field label={t('backend.accounts.domain')} error={errors.domain?.message}>
          <input type="text" className="input" {...register('domain', { required: t('validation.required') })} />
        </Field>
        <Field label={t('auth.password')} error={errors.password?.message}>
          <input
            type="password"
            className="input"
            {...register('password', { required: t('validation.required'), minLength: { value: 8, message: t('validation.minLength', { min: 8 }) } })}
          />
        </Field>
        <div className="flex justify-end gap-3 pt-2">
          <button type="button" onClick={onClose} className="btn btn-secondary">{t('common.cancel')}</button>
          <button type="submit" disabled={isSubmitting} className="btn btn-primary">{t('common.create')}</button>
        </div>
      </form>
    </Modal>
  )
}

function SetPasswordModal({ server, account, onClose }: { server: Server; account: Account; onClose: () => void }) {
  const { t } = useTranslation()
  const { register, handleSubmit, formState: { errors, isSubmitting } } = useForm<{ password: string }>()
  const onSubmit = async ({ password }: { password: string }) => {
    try {
      await backendApi.setPassword(server.id, account.id, password)
      toast.success(t('backend.accounts.updated'))
      onClose()
    } catch (error) {
      toast.error(errorMessage(error) ?? t('backend.accounts.updateFailed'))
    }
  }
  return (
    <Modal title={t('backend.accounts.setPasswordTitle', { id: account.id })} onClose={onClose}>
      <form onSubmit={handleSubmit(onSubmit)} className="space-y-4">
        <Field label={t('backend.accounts.newPassword')} error={errors.password?.message}>
          <input
            type="password"
            className="input"
            autoFocus
            {...register('password', { required: t('validation.required'), minLength: { value: 8, message: t('validation.minLength', { min: 8 }) } })}
          />
        </Field>
        <div className="flex justify-end gap-3 pt-2">
          <button type="button" onClick={onClose} className="btn btn-secondary">{t('common.cancel')}</button>
          <button type="submit" disabled={isSubmitting} className="btn btn-primary">{t('common.save')}</button>
        </div>
      </form>
    </Modal>
  )
}

function ImportCsvModal({ server, onClose, onDone }: { server: Server; onClose: () => void; onDone: () => void }) {
  const { t } = useTranslation()
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async () => {
    const rows = parseAccountsCsv(text, server.domain)
    if (rows.length === 0) {
      toast.error(t('backend.accounts.importNoRows'))
      return
    }
    setBusy(true)
    let ok = 0
    let failed = 0
    let i = 0
    const worker = async () => {
      while (i < rows.length) {
        const row = rows[i++]
        try {
          await backendApi.createAccount(server.id, row)
          ok++
        } catch {
          failed++
        }
      }
    }
    await Promise.all(Array.from({ length: 4 }, worker))
    setBusy(false)
    if (failed === 0) toast.success(t('backend.accounts.imported', { count: ok }))
    else toast.error(t('backend.accounts.importPartial', { ok, failed }))
    onDone()
  }

  return (
    <Modal title={t('backend.accounts.importTitle')} onClose={onClose} width="max-w-xl">
      <div className="space-y-3">
        <p className="text-sm text-gray-400">
          <Trans i18nKey="backend.accounts.importHelp" components={{ code: <code className="bg-gray-700 px-1 rounded text-xs" /> }} />
        </p>
        <input
          type="file"
          accept=".csv,text/csv,text/plain"
          onChange={async (e) => {
            const file = e.target.files?.[0]
            if (file) setText(await file.text())
          }}
          className="text-sm text-gray-300 file:mr-3 file:px-3 file:py-1 file:rounded file:border-0 file:bg-gray-700 file:text-white"
        />
        <textarea
          className="input font-mono text-sm h-48 resize-y"
          placeholder={'localpart,password\nalice,Strongpw123\nbob,example.com,AnotherPw456'}
          value={text}
          onChange={(e) => setText(e.target.value)}
        />
        <div className="flex justify-end gap-2 pt-2">
          <button onClick={onClose} className="btn btn-secondary" disabled={busy}>{t('common.cancel')}</button>
          <button onClick={submit} disabled={busy || !text.trim()} className="btn btn-primary">
            {busy ? '...' : t('backend.accounts.importConfirm')}
          </button>
        </div>
      </div>
    </Modal>
  )
}
