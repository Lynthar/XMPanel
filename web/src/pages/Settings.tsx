import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useAuthStore } from '@/store/auth'
import { authApi } from '@/lib/api'
import { useForm } from 'react-hook-form'
import toast from 'react-hot-toast'
import {
  User,
  Lock,
  Shield,
  Smartphone,
  Copy,
  Check,
  AlertTriangle,
} from 'lucide-react'

interface PasswordForm {
  current_password: string
  new_password: string
  confirm_password: string
}

interface MFASetupData {
  secret: string
  uri: string
}

export default function Settings() {
  const { t } = useTranslation()
  const { user } = useAuthStore()

  return (
    <div className="space-y-6 max-w-2xl">
      {/* Page header */}
      <div>
        <h1 className="text-2xl font-bold text-white">{t('settings.title')}</h1>
        <p className="text-gray-400 mt-1">{t('settings.subtitle')}</p>
      </div>

      {/* Profile section */}
      <section className="card">
        <div className="flex items-center gap-4 mb-6">
          <div className="p-3 rounded-lg bg-primary-600/20">
            <User className="w-6 h-6 text-primary-400" />
          </div>
          <div>
            <h2 className="text-lg font-semibold text-white">{t('settings.profile.title')}</h2>
            <p className="text-sm text-gray-400">{t('settings.profile.subtitle')}</p>
          </div>
        </div>

        <div className="space-y-4">
          <div className="flex justify-between py-3 border-b border-gray-700">
            <span className="text-gray-400">{t('settings.profile.username')}</span>
            <span className="text-white">{user?.username}</span>
          </div>
          <div className="flex justify-between py-3 border-b border-gray-700">
            <span className="text-gray-400">{t('settings.profile.email')}</span>
            <span className="text-white">{user?.email}</span>
          </div>
          <div className="flex justify-between py-3">
            <span className="text-gray-400">{t('settings.profile.role')}</span>
            <span className="text-white">
              {user?.role
                ? t(`users.roles.${user.role}`, { defaultValue: user.role })
                : ''}
            </span>
          </div>
        </div>
      </section>

      {/* Change Password section */}
      <ChangePasswordSection />

      {/* MFA section */}
      <MFASection />
    </div>
  )
}

function ChangePasswordSection() {
  const { t } = useTranslation()
  const [loading, setLoading] = useState(false)
  const { register, handleSubmit, reset, formState: { errors }, watch } = useForm<PasswordForm>()

  const newPassword = watch('new_password')

  const onSubmit = async (data: PasswordForm) => {
    if (data.new_password !== data.confirm_password) {
      toast.error(t('settings.security.passwordsDoNotMatch'))
      return
    }

    setLoading(true)
    try {
      await authApi.changePassword(data.current_password, data.new_password)
      toast.success(t('settings.security.changePasswordSuccess'))
      reset()
    } catch (error: unknown) {
      const err = error as { response?: { data?: { error?: string } } }
      toast.error(err.response?.data?.error || t('settings.security.changePasswordFailed'))
    } finally {
      setLoading(false)
    }
  }

  return (
    <section className="card">
      <div className="flex items-center gap-4 mb-6">
        <div className="p-3 rounded-lg bg-yellow-600/20">
          <Lock className="w-6 h-6 text-yellow-400" />
        </div>
        <div>
          <h2 className="text-lg font-semibold text-white">{t('settings.security.changePassword')}</h2>
          <p className="text-sm text-gray-400">{t('settings.security.changePasswordDesc')}</p>
        </div>
      </div>

      <form onSubmit={handleSubmit(onSubmit)} className="space-y-4">
        <div>
          <label className="block text-sm font-medium text-gray-300 mb-1">{t('auth.currentPassword')}</label>
          <input
            type="password"
            className="input"
            {...register('current_password', { required: t('validation.required') })}
          />
          {errors.current_password && (
            <p className="mt-1 text-sm text-red-400">{errors.current_password.message}</p>
          )}
        </div>

        <div>
          <label className="block text-sm font-medium text-gray-300 mb-1">{t('auth.newPassword')}</label>
          <input
            type="password"
            className="input"
            {...register('new_password', {
              required: t('validation.required'),
              minLength: { value: 12, message: t('validation.minLength', { min: 12 }) },
            })}
          />
          {errors.new_password && (
            <p className="mt-1 text-sm text-red-400">{errors.new_password.message}</p>
          )}
        </div>

        <div>
          <label className="block text-sm font-medium text-gray-300 mb-1">{t('auth.confirmPassword')}</label>
          <input
            type="password"
            className="input"
            {...register('confirm_password', {
              required: t('validation.required'),
              validate: (value) => value === newPassword || t('validation.passwordMatch'),
            })}
          />
          {errors.confirm_password && (
            <p className="mt-1 text-sm text-red-400">{errors.confirm_password.message}</p>
          )}
        </div>

        <button type="submit" disabled={loading} className="btn btn-primary">
          {loading ? t('settings.security.changing') : t('settings.security.changePassword')}
        </button>
      </form>
    </section>
  )
}

function MFASection() {
  const { t } = useTranslation()
  const { user, setUser } = useAuthStore()
  const [setupData, setSetupData] = useState<MFASetupData | null>(null)
  const [verificationCode, setVerificationCode] = useState('')
  const [recoveryCodes, setRecoveryCodes] = useState<string[]>([])
  const [loading, setLoading] = useState(false)
  const [copied, setCopied] = useState(false)
  const [disablePromptOpen, setDisablePromptOpen] = useState(false)
  const [disablePassword, setDisablePassword] = useState('')
  const [disableCode, setDisableCode] = useState('')

  const handleSetupMFA = async () => {
    setLoading(true)
    try {
      const response = await authApi.setupMFA()
      setSetupData(response.data)
    } catch (error: unknown) {
      const err = error as { response?: { data?: { error?: string } } }
      toast.error(err.response?.data?.error || t('settings.security.mfaSetupFailed'))
    } finally {
      setLoading(false)
    }
  }

  const handleVerifyMFA = async () => {
    if (verificationCode.length !== 6) {
      toast.error(t('settings.security.mfaEnter6Digit'))
      return
    }

    setLoading(true)
    try {
      const response = await authApi.verifyMFA(verificationCode)
      setRecoveryCodes(response.data.recovery_codes)
      toast.success(t('settings.security.mfaSetupSuccess'))
      setSetupData(null)
      // Refresh user so mfa_enabled flips locally
      try {
        const me = await authApi.me()
        setUser(me.data)
      } catch {
        /* non-fatal: user can re-login to refresh state */
      }
    } catch (error: unknown) {
      const err = error as { response?: { data?: { error?: string } } }
      toast.error(err.response?.data?.error || t('settings.security.mfaInvalidCode'))
    } finally {
      setLoading(false)
    }
  }

  const handleDisableMFA = async () => {
    if (!disablePassword || disableCode.length !== 6) {
      toast.error(t('settings.security.mfaEnter6Digit'))
      return
    }
    setLoading(true)
    try {
      await authApi.disableMFA(disablePassword, disableCode)
      toast.success(t('settings.security.mfaDisableSuccess'))
      setDisablePromptOpen(false)
      setDisablePassword('')
      setDisableCode('')
      try {
        const me = await authApi.me()
        setUser(me.data)
      } catch {
        /* non-fatal */
      }
    } catch (error: unknown) {
      const err = error as { response?: { data?: { error?: string } } }
      toast.error(err.response?.data?.error || t('settings.security.mfaDisableFailed'))
    } finally {
      setLoading(false)
    }
  }

  const copySecret = () => {
    if (setupData?.secret) {
      navigator.clipboard.writeText(setupData.secret)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    }
  }

  return (
    <section className="card">
      <div className="flex items-center gap-4 mb-6">
        <div className="p-3 rounded-lg bg-green-600/20">
          <Shield className="w-6 h-6 text-green-400" />
        </div>
        <div>
          <h2 className="text-lg font-semibold text-white">{t('settings.security.mfa')}</h2>
          <p className="text-sm text-gray-400">{t('settings.security.mfaDesc')}</p>
        </div>
      </div>

      {user?.mfa_enabled ? (
        <div className="space-y-4">
          <div className="flex items-center justify-between gap-3 p-4 bg-green-900/20 border border-green-800 rounded-lg">
            <div className="flex items-center gap-3">
              <Shield className="w-5 h-5 text-green-400" />
              <span className="text-green-400">{t('settings.security.mfaIsEnabled')}</span>
            </div>
            {!disablePromptOpen && (
              <button
                onClick={() => setDisablePromptOpen(true)}
                className="text-sm text-red-400 hover:text-red-300 underline"
              >
                {t('auth.mfaDisable')}
              </button>
            )}
          </div>

          {disablePromptOpen && (
            <div className="space-y-4 p-4 bg-red-900/20 border border-red-800 rounded-lg">
              <div className="flex items-start gap-3">
                <AlertTriangle className="w-5 h-5 text-red-400 mt-0.5 flex-shrink-0" />
                <p className="text-sm text-gray-300">{t('settings.security.mfaDisableTitle')}</p>
              </div>

              <div>
                <label className="block text-sm font-medium text-gray-300 mb-1">{t('auth.currentPassword')}</label>
                <input
                  type="password"
                  className="input"
                  value={disablePassword}
                  onChange={(e) => setDisablePassword(e.target.value)}
                  autoComplete="current-password"
                />
              </div>

              <div>
                <label className="block text-sm font-medium text-gray-300 mb-1">{t('settings.security.mfaVerificationCode')}</label>
                <input
                  type="text"
                  inputMode="numeric"
                  maxLength={6}
                  className="input w-32 text-center text-xl tracking-widest"
                  placeholder="000000"
                  value={disableCode}
                  onChange={(e) => setDisableCode(e.target.value.replace(/\D/g, ''))}
                  autoComplete="one-time-code"
                />
              </div>

              <div className="flex gap-2">
                <button
                  onClick={handleDisableMFA}
                  disabled={loading || !disablePassword || disableCode.length !== 6}
                  className="btn btn-primary"
                >
                  {loading ? t('settings.security.mfaDisabling') : t('settings.security.mfaConfirmDisable')}
                </button>
                <button
                  onClick={() => {
                    setDisablePromptOpen(false)
                    setDisablePassword('')
                    setDisableCode('')
                  }}
                  className="btn btn-secondary"
                >
                  {t('common.cancel')}
                </button>
              </div>
            </div>
          )}
        </div>
      ) : setupData ? (
        <div className="space-y-6">
          <div className="p-4 bg-yellow-900/20 border border-yellow-800 rounded-lg">
            <div className="flex items-start gap-3">
              <AlertTriangle className="w-5 h-5 text-yellow-400 mt-0.5" />
              <div>
                <p className="text-yellow-400 font-medium">{t('settings.security.mfaSetupInstructionsTitle')}</p>
                <ol className="text-sm text-gray-400 mt-2 list-decimal list-inside space-y-1">
                  <li>{t('settings.security.mfaSetupStep1')}</li>
                  <li>{t('settings.security.mfaSetupStep2')}</li>
                  <li>{t('settings.security.mfaSetupStep3')}</li>
                </ol>
              </div>
            </div>
          </div>

          <div className="flex items-center gap-4">
            <Smartphone className="w-12 h-12 text-gray-400" />
            <div>
              <p className="text-sm text-gray-400 mb-1">{t('settings.security.mfaSecretKey')}</p>
              <div className="flex items-center gap-2">
                <code className="px-3 py-1 bg-gray-700 rounded text-sm font-mono text-white">
                  {setupData.secret}
                </code>
                <button onClick={copySecret} className="p-1 text-gray-400 hover:text-white">
                  {copied ? <Check className="w-4 h-4 text-green-400" /> : <Copy className="w-4 h-4" />}
                </button>
              </div>
            </div>
          </div>

          <div>
            <label className="block text-sm font-medium text-gray-300 mb-1">{t('settings.security.mfaVerificationCode')}</label>
            <div className="flex gap-2">
              <input
                type="text"
                inputMode="numeric"
                maxLength={6}
                className="input w-32 text-center text-xl tracking-widest"
                placeholder="000000"
                value={verificationCode}
                onChange={(e) => setVerificationCode(e.target.value.replace(/\D/g, ''))}
              />
              <button
                onClick={handleVerifyMFA}
                disabled={loading || verificationCode.length !== 6}
                className="btn btn-primary"
              >
                {loading ? t('settings.security.mfaVerifying') : t('settings.security.mfaVerifyEnable')}
              </button>
            </div>
          </div>
        </div>
      ) : recoveryCodes.length > 0 ? (
        <div className="space-y-4">
          <div className="p-4 bg-red-900/20 border border-red-800 rounded-lg">
            <div className="flex items-start gap-3">
              <AlertTriangle className="w-5 h-5 text-red-400 mt-0.5" />
              <div>
                <p className="text-red-400 font-medium">{t('settings.security.mfaSaveCodesTitle')}</p>
                <p className="text-sm text-gray-400 mt-1">{t('settings.security.mfaSaveCodesDesc')}</p>
              </div>
            </div>
          </div>

          <div className="grid grid-cols-2 gap-2 p-4 bg-gray-700 rounded-lg">
            {recoveryCodes.map((code, i) => (
              <code key={i} className="text-sm font-mono text-white">{code}</code>
            ))}
          </div>

          <button
            onClick={() => setRecoveryCodes([])}
            className="btn btn-primary"
          >
            {t('settings.security.mfaSavedCodes')}
          </button>
        </div>
      ) : (
        <button onClick={handleSetupMFA} disabled={loading} className="btn btn-primary">
          {loading ? t('settings.security.mfaSettingUp') : t('settings.security.mfaEnable')}
        </button>
      )}
    </section>
  )
}
