import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation, Trans } from 'react-i18next'
import { usersApi } from '@/lib/api'
import { useAuthStore } from '@/store/auth'
import { useForm } from 'react-hook-form'
import toast from 'react-hot-toast'
import {
  Users as UsersIcon,
  Plus,
  Pencil,
  Trash2,
  X,
  Shield,
  ShieldCheck,
} from 'lucide-react'
import clsx from 'clsx'
import ConfirmDialog from '@/components/ConfirmDialog'

interface User {
  id: number
  username: string
  email: string
  role: string
  mfa_enabled: boolean
  last_login_at?: string
  created_at: string
}

interface CreateUserForm {
  username: string
  email: string
  password: string
  role: string
}

interface EditUserForm {
  email?: string
  password?: string
  role?: string
}

export default function Users() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const { user: currentUser } = useAuthStore()
  const [showAddModal, setShowAddModal] = useState(false)
  const [editingUser, setEditingUser] = useState<User | null>(null)
  const [pendingDelete, setPendingDelete] = useState<User | null>(null)

  const { data: users, isLoading } = useQuery({
    queryKey: ['users'],
    queryFn: async () => {
      const response = await usersApi.list()
      return response.data as User[]
    },
  })

  const deleteMutation = useMutation({
    mutationFn: (id: number) => usersApi.delete(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['users'] })
      toast.success(t('users.deleteSuccess'))
    },
    onError: (error: unknown) => {
      const err = error as { response?: { data?: { error?: string } } }
      toast.error(err.response?.data?.error || t('users.deleteFailed'))
    },
    onSettled: () => setPendingDelete(null),
  })

  const handleDelete = (user: User) => {
    if (user.id === currentUser?.id) {
      toast.error(t('users.cannotDeleteSelf'))
      return
    }
    setPendingDelete(user)
  }

  const roleColors: Record<string, string> = {
    superadmin: 'badge-red',
    admin: 'badge-blue',
    operator: 'badge-yellow',
    viewer: 'badge-gray',
    auditor: 'badge-green',
  }

  return (
    <div className="space-y-6">
      {/* Page header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold text-white">{t('users.title')}</h1>
          <p className="text-gray-400 mt-1">{t('users.subtitle')}</p>
        </div>
        <button onClick={() => setShowAddModal(true)} className="btn btn-primary flex items-center gap-2">
          <Plus className="w-4 h-4" />
          {t('users.addUser')}
        </button>
      </div>

      {/* Users table */}
      <div className="card">
        {isLoading ? (
          <div className="flex items-center justify-center py-12">
            <div className="animate-spin rounded-full h-8 w-8 border-b-2 border-primary-500" />
          </div>
        ) : users?.length === 0 ? (
          <div className="text-center py-12">
            <UsersIcon className="w-12 h-12 text-gray-600 mx-auto mb-4" />
            <p className="text-gray-400">{t('users.noUsersFound')}</p>
          </div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full">
              <thead>
                <tr className="border-b border-gray-700">
                  <th className="table-header">{t('users.username')}</th>
                  <th className="table-header">{t('users.role')}</th>
                  <th className="table-header">{t('settings.security.mfa')}</th>
                  <th className="table-header">{t('users.lastLogin')}</th>
                  <th className="table-header">{t('common.actions')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-700">
                {users?.map((user) => (
                  <tr key={user.id} className="hover:bg-gray-700/50">
                    <td className="table-cell">
                      <div>
                        <p className="font-medium text-white">{user.username}</p>
                        <p className="text-sm text-gray-400">{user.email}</p>
                      </div>
                    </td>
                    <td className="table-cell">
                      <span className={clsx('badge', roleColors[user.role] || 'badge-gray')}>
                        {t(`users.roles.${user.role}`, { defaultValue: user.role })}
                      </span>
                    </td>
                    <td className="table-cell">
                      {user.mfa_enabled ? (
                        <span title={t('users.mfaEnabled')}><ShieldCheck className="w-5 h-5 text-green-400" /></span>
                      ) : (
                        <span><Shield className="w-5 h-5 text-gray-500" /></span>
                      )}
                    </td>
                    <td className="table-cell text-gray-400">
                      {user.last_login_at
                        ? new Date(user.last_login_at).toLocaleString()
                        : t('users.never')}
                    </td>
                    <td className="table-cell">
                      <div className="flex gap-2">
                        <button
                          onClick={() => setEditingUser(user)}
                          className="p-1 text-gray-400 hover:text-white"
                          title={t('common.edit')}
                        >
                          <Pencil className="w-4 h-4" />
                        </button>
                        <button
                          onClick={() => handleDelete(user)}
                          className="p-1 text-gray-400 hover:text-red-400"
                          title={t('common.delete')}
                          disabled={user.id === currentUser?.id}
                        >
                          <Trash2 className="w-4 h-4" />
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {/* Add user modal */}
      {showAddModal && (
        <AddUserModal
          onClose={() => setShowAddModal(false)}
          onSuccess={() => {
            setShowAddModal(false)
            queryClient.invalidateQueries({ queryKey: ['users'] })
          }}
        />
      )}

      {/* Edit user modal */}
      {editingUser && (
        <EditUserModal
          user={editingUser}
          onClose={() => setEditingUser(null)}
          onSuccess={() => {
            setEditingUser(null)
            queryClient.invalidateQueries({ queryKey: ['users'] })
          }}
        />
      )}

      <ConfirmDialog
        open={pendingDelete !== null}
        title={t('users.confirmDelete')}
        message={
          <Trans
            i18nKey="users.deleteUserPrompt"
            values={{ username: pendingDelete?.username ?? '' }}
            components={{ strong: <span className="font-semibold text-white" /> }}
          />
        }
        confirmLabel={t('common.delete')}
        cancelLabel={t('common.cancel')}
        variant="danger"
        loading={deleteMutation.isPending}
        onConfirm={() => {
          if (pendingDelete) deleteMutation.mutate(pendingDelete.id)
        }}
        onCancel={() => setPendingDelete(null)}
      />
    </div>
  )
}

function AddUserModal({ onClose, onSuccess }: { onClose: () => void; onSuccess: () => void }) {
  const { t } = useTranslation()
  const [loading, setLoading] = useState(false)
  const { register, handleSubmit, formState: { errors } } = useForm<CreateUserForm>({
    defaultValues: { role: 'viewer' },
  })

  const onSubmit = async (data: CreateUserForm) => {
    setLoading(true)
    try {
      await usersApi.create(data)
      toast.success(t('users.createSuccess'))
      onSuccess()
    } catch (error: unknown) {
      const err = error as { response?: { data?: { error?: string } } }
      toast.error(err.response?.data?.error || t('users.createFailed'))
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/50">
      <div className="w-full max-w-md bg-gray-800 rounded-xl border border-gray-700">
        <div className="flex items-center justify-between p-4 border-b border-gray-700">
          <h2 className="text-lg font-semibold text-white">{t('users.addUser')}</h2>
          <button onClick={onClose} className="text-gray-400 hover:text-white">
            <X className="w-5 h-5" />
          </button>
        </div>

        <form onSubmit={handleSubmit(onSubmit)} className="p-4 space-y-4">
          <div>
            <label className="block text-sm font-medium text-gray-300 mb-1">{t('users.username')}</label>
            <input
              type="text"
              className="input"
              {...register('username', {
                required: t('validation.required'),
                minLength: { value: 3, message: t('validation.minLength', { min: 3 }) },
              })}
            />
            {errors.username && <p className="mt-1 text-sm text-red-400">{errors.username.message}</p>}
          </div>

          <div>
            <label className="block text-sm font-medium text-gray-300 mb-1">{t('users.email')}</label>
            <input
              type="email"
              className="input"
              {...register('email', { required: t('validation.required') })}
            />
            {errors.email && <p className="mt-1 text-sm text-red-400">{errors.email.message}</p>}
          </div>

          <div>
            <label className="block text-sm font-medium text-gray-300 mb-1">{t('auth.password')}</label>
            <input
              type="password"
              className="input"
              {...register('password', {
                required: t('validation.required'),
                minLength: { value: 12, message: t('validation.minLength', { min: 12 }) },
              })}
            />
            {errors.password && <p className="mt-1 text-sm text-red-400">{errors.password.message}</p>}
          </div>

          <div>
            <label className="block text-sm font-medium text-gray-300 mb-1">{t('users.role')}</label>
            <select className="input" {...register('role')}>
              <option value="viewer">{t('users.roles.viewer')}</option>
              <option value="operator">{t('users.roles.operator')}</option>
              <option value="admin">{t('users.roles.admin')}</option>
              <option value="auditor">{t('users.roles.auditor')}</option>
            </select>
          </div>

          <div className="flex justify-end gap-3 pt-4">
            <button type="button" onClick={onClose} className="btn btn-secondary">{t('common.cancel')}</button>
            <button type="submit" disabled={loading} className="btn btn-primary">
              {loading ? '...' : t('users.createUser')}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}

function EditUserModal({ user, onClose, onSuccess }: { user: User; onClose: () => void; onSuccess: () => void }) {
  const { t } = useTranslation()
  const [loading, setLoading] = useState(false)
  const { register, handleSubmit } = useForm<EditUserForm>({
    defaultValues: { email: user.email, role: user.role },
  })

  const onSubmit = async (data: EditUserForm) => {
    setLoading(true)
    try {
      const updateData: EditUserForm = {}
      if (data.email && data.email !== user.email) updateData.email = data.email
      if (data.password) updateData.password = data.password
      if (data.role && data.role !== user.role) updateData.role = data.role

      if (Object.keys(updateData).length === 0) {
        toast(t('users.noChangesToSave'))
        onClose()
        return
      }

      await usersApi.update(user.id, updateData)
      toast.success(t('users.updateSuccess'))
      onSuccess()
    } catch (error: unknown) {
      const err = error as { response?: { data?: { error?: string } } }
      toast.error(err.response?.data?.error || t('users.updateFailed'))
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/50">
      <div className="w-full max-w-md bg-gray-800 rounded-xl border border-gray-700">
        <div className="flex items-center justify-between p-4 border-b border-gray-700">
          <h2 className="text-lg font-semibold text-white">
            {t('users.editUserTitle', { username: user.username })}
          </h2>
          <button onClick={onClose} className="text-gray-400 hover:text-white">
            <X className="w-5 h-5" />
          </button>
        </div>

        <form onSubmit={handleSubmit(onSubmit)} className="p-4 space-y-4">
          <div>
            <label className="block text-sm font-medium text-gray-300 mb-1">{t('users.email')}</label>
            <input type="email" className="input" {...register('email')} />
          </div>

          <div>
            <label className="block text-sm font-medium text-gray-300 mb-1">
              {t('users.newPassword')}{' '}
              <span className="text-gray-500">({t('users.newPasswordHint')})</span>
            </label>
            <input type="password" className="input" {...register('password')} />
          </div>

          <div>
            <label className="block text-sm font-medium text-gray-300 mb-1">{t('users.role')}</label>
            <select className="input" {...register('role')}>
              <option value="viewer">{t('users.roles.viewer')}</option>
              <option value="operator">{t('users.roles.operator')}</option>
              <option value="admin">{t('users.roles.admin')}</option>
              <option value="auditor">{t('users.roles.auditor')}</option>
            </select>
          </div>

          <div className="flex justify-end gap-3 pt-4">
            <button type="button" onClick={onClose} className="btn btn-secondary">{t('common.cancel')}</button>
            <button type="submit" disabled={loading} className="btn btn-primary">
              {loading ? '...' : t('users.saveChanges')}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
