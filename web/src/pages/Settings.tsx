import { useState } from 'react'
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
  const { user } = useAuthStore()

  return (
    <div className="space-y-6 max-w-2xl">
      {/* Page header */}
      <div>
        <h1 className="text-2xl font-bold text-white">Settings</h1>
        <p className="text-gray-400 mt-1">Manage your account settings and security</p>
      </div>

      {/* Profile section */}
      <section className="card">
        <div className="flex items-center gap-4 mb-6">
          <div className="p-3 rounded-lg bg-primary-600/20">
            <User className="w-6 h-6 text-primary-400" />
          </div>
          <div>
            <h2 className="text-lg font-semibold text-white">Profile</h2>
            <p className="text-sm text-gray-400">Your account information</p>
          </div>
        </div>

        <div className="space-y-4">
          <div className="flex justify-between py-3 border-b border-gray-700">
            <span className="text-gray-400">Username</span>
            <span className="text-white">{user?.username}</span>
          </div>
          <div className="flex justify-between py-3 border-b border-gray-700">
            <span className="text-gray-400">Email</span>
            <span className="text-white">{user?.email}</span>
          </div>
          <div className="flex justify-between py-3">
            <span className="text-gray-400">Role</span>
            <span className="text-white capitalize">{user?.role}</span>
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
  const [loading, setLoading] = useState(false)
  const { register, handleSubmit, reset, formState: { errors }, watch } = useForm<PasswordForm>()

  const newPassword = watch('new_password')

  const onSubmit = async (data: PasswordForm) => {
    if (data.new_password !== data.confirm_password) {
      toast.error('Passwords do not match')
      return
    }

    setLoading(true)
    try {
      await authApi.changePassword(data.current_password, data.new_password)
      toast.success('Password changed successfully')
      reset()
    } catch (error: unknown) {
      const err = error as { response?: { data?: { error?: string } } }
      toast.error(err.response?.data?.error || 'Failed to change password')
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
          <h2 className="text-lg font-semibold text-white">Change Password</h2>
          <p className="text-sm text-gray-400">Update your password regularly for security</p>
        </div>
      </div>

      <form onSubmit={handleSubmit(onSubmit)} className="space-y-4">
        <div>
          <label className="block text-sm font-medium text-gray-300 mb-1">Current Password</label>
          <input
            type="password"
            className="input"
            {...register('current_password', { required: 'Required' })}
          />
          {errors.current_password && (
            <p className="mt-1 text-sm text-red-400">{errors.current_password.message}</p>
          )}
        </div>

        <div>
          <label className="block text-sm font-medium text-gray-300 mb-1">New Password</label>
          <input
            type="password"
            className="input"
            {...register('new_password', {
              required: 'Required',
              minLength: { value: 12, message: 'Minimum 12 characters' },
            })}
          />
          {errors.new_password && (
            <p className="mt-1 text-sm text-red-400">{errors.new_password.message}</p>
          )}
        </div>

        <div>
          <label className="block text-sm font-medium text-gray-300 mb-1">Confirm New Password</label>
          <input
            type="password"
            className="input"
            {...register('confirm_password', {
              required: 'Required',
              validate: (value) => value === newPassword || 'Passwords do not match',
            })}
          />
          {errors.confirm_password && (
            <p className="mt-1 text-sm text-red-400">{errors.confirm_password.message}</p>
          )}
        </div>

        <button type="submit" disabled={loading} className="btn btn-primary">
          {loading ? 'Changing...' : 'Change Password'}
        </button>
      </form>
    </section>
  )
}

function MFASection() {
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
      toast.error(err.response?.data?.error || 'Failed to setup MFA')
    } finally {
      setLoading(false)
    }
  }

  const handleVerifyMFA = async () => {
    if (verificationCode.length !== 6) {
      toast.error('Please enter a 6-digit code')
      return
    }

    setLoading(true)
    try {
      const response = await authApi.verifyMFA(verificationCode)
      setRecoveryCodes(response.data.recovery_codes)
      toast.success('MFA enabled successfully!')
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
      toast.error(err.response?.data?.error || 'Invalid verification code')
    } finally {
      setLoading(false)
    }
  }

  const handleDisableMFA = async () => {
    if (!disablePassword || disableCode.length !== 6) {
      toast.error('Enter your password and a 6-digit code')
      return
    }
    setLoading(true)
    try {
      await authApi.disableMFA(disablePassword, disableCode)
      toast.success('MFA disabled')
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
      toast.error(err.response?.data?.error || 'Failed to disable MFA')
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
          <h2 className="text-lg font-semibold text-white">Two-Factor Authentication</h2>
          <p className="text-sm text-gray-400">Add an extra layer of security to your account</p>
        </div>
      </div>

      {user?.mfa_enabled ? (
        <div className="space-y-4">
          <div className="flex items-center justify-between gap-3 p-4 bg-green-900/20 border border-green-800 rounded-lg">
            <div className="flex items-center gap-3">
              <Shield className="w-5 h-5 text-green-400" />
              <span className="text-green-400">Two-factor authentication is enabled</span>
            </div>
            {!disablePromptOpen && (
              <button
                onClick={() => setDisablePromptOpen(true)}
                className="text-sm text-red-400 hover:text-red-300 underline"
              >
                Disable
              </button>
            )}
          </div>

          {disablePromptOpen && (
            <div className="space-y-4 p-4 bg-red-900/20 border border-red-800 rounded-lg">
              <div className="flex items-start gap-3">
                <AlertTriangle className="w-5 h-5 text-red-400 mt-0.5 flex-shrink-0" />
                <p className="text-sm text-gray-300">
                  Disabling two-factor authentication weakens your account security. Confirm with your
                  current password and a code from your authenticator.
                </p>
              </div>

              <div>
                <label className="block text-sm font-medium text-gray-300 mb-1">Current Password</label>
                <input
                  type="password"
                  className="input"
                  value={disablePassword}
                  onChange={(e) => setDisablePassword(e.target.value)}
                  autoComplete="current-password"
                />
              </div>

              <div>
                <label className="block text-sm font-medium text-gray-300 mb-1">Verification Code</label>
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
                  {loading ? 'Disabling...' : 'Confirm Disable'}
                </button>
                <button
                  onClick={() => {
                    setDisablePromptOpen(false)
                    setDisablePassword('')
                    setDisableCode('')
                  }}
                  className="btn btn-secondary"
                >
                  Cancel
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
                <p className="text-yellow-400 font-medium">Setup Instructions</p>
                <ol className="text-sm text-gray-400 mt-2 list-decimal list-inside space-y-1">
                  <li>Download an authenticator app (Google Authenticator, Authy, etc.)</li>
                  <li>Scan the QR code or enter the secret key manually</li>
                  <li>Enter the 6-digit code from the app below</li>
                </ol>
              </div>
            </div>
          </div>

          <div className="flex items-center gap-4">
            <Smartphone className="w-12 h-12 text-gray-400" />
            <div>
              <p className="text-sm text-gray-400 mb-1">Secret Key:</p>
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
            <label className="block text-sm font-medium text-gray-300 mb-1">Verification Code</label>
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
                {loading ? 'Verifying...' : 'Verify & Enable'}
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
                <p className="text-red-400 font-medium">Save Your Recovery Codes</p>
                <p className="text-sm text-gray-400 mt-1">
                  Store these codes in a safe place. You can use them to access your account if you lose your authenticator.
                </p>
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
            I've Saved My Codes
          </button>
        </div>
      ) : (
        <button onClick={handleSetupMFA} disabled={loading} className="btn btn-primary">
          {loading ? 'Setting up...' : 'Enable Two-Factor Authentication'}
        </button>
      )}
    </section>
  )
}
