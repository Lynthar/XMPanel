import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { authApi } from '@/lib/api'
import { useAuthStore } from '@/store/auth'
import toast from 'react-hot-toast'
import { Eye, EyeOff, Shield, Lock, Globe } from 'lucide-react'

interface LoginForm {
  username: string
  password: string
  totp_code?: string
}

export default function Login() {
  const navigate = useNavigate()
  const { t, i18n } = useTranslation()
  const { setAuth } = useAuthStore()
  const [showPassword, setShowPassword] = useState(false)
  const [mfaRequired, setMfaRequired] = useState(false)
  const [loading, setLoading] = useState(false)

  const {
    register,
    handleSubmit,
    formState: { errors },
  } = useForm<LoginForm>()

  const toggleLanguage = () => {
    const newLang = i18n.language === 'en' ? 'zh' : 'en'
    i18n.changeLanguage(newLang)
  }

  const onSubmit = async (data: LoginForm) => {
    setLoading(true)
    try {
      const response = await authApi.login(data.username, data.password, data.totp_code)

      if (response.data.mfa_required) {
        setMfaRequired(true)
        toast(t('auth.mfaDescription'), { icon: '🔐' })
      } else {
        // refresh token is delivered via HttpOnly cookie set by the server
        setAuth(response.data.user, response.data.access_token)
        toast.success(t('common.success'))
        navigate('/')
      }
    } catch (error: unknown) {
      const err = error as { response?: { data?: { error?: string } } }
      toast.error(err.response?.data?.error || t('auth.invalidCredentials'))
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center bg-gray-900 px-4">
      {/* Language switcher */}
      <button
        onClick={toggleLanguage}
        className="absolute top-4 right-4 flex items-center gap-2 text-gray-400 hover:text-white transition-colors"
      >
        <Globe className="w-5 h-5" />
        <span className="text-sm">{i18n.language === 'en' ? '中文' : 'English'}</span>
      </button>

      <div className="w-full max-w-md">
        {/* Logo */}
        <div className="text-center mb-8">
          <div className="inline-flex items-center justify-center w-16 h-16 rounded-2xl bg-primary-600 mb-4">
            <Shield className="w-8 h-8 text-white" />
          </div>
          <h1 className="text-3xl font-bold text-white">XMPanel</h1>
          <p className="text-gray-400 mt-2">{t('auth.loginTitle')}</p>
        </div>

        {/* Login form */}
        <div className="card">
          <form onSubmit={handleSubmit(onSubmit)} className="space-y-6">
            {/* Username */}
            <div>
              <label htmlFor="username" className="block text-sm font-medium text-gray-300 mb-2">
                {t('auth.username')}
              </label>
              <input
                id="username"
                type="text"
                autoComplete="username"
                className="input"
                placeholder={t('auth.username')}
                {...register('username', { required: t('validation.required') })}
              />
              {errors.username && (
                <p className="mt-1 text-sm text-red-400">{errors.username.message}</p>
              )}
            </div>

            {/* Password */}
            <div>
              <label htmlFor="password" className="block text-sm font-medium text-gray-300 mb-2">
                {t('auth.password')}
              </label>
              <div className="relative">
                <input
                  id="password"
                  type={showPassword ? 'text' : 'password'}
                  autoComplete="current-password"
                  className="input pr-12"
                  placeholder={t('auth.password')}
                  {...register('password', { required: t('validation.required') })}
                />
                <button
                  type="button"
                  className="absolute right-3 top-1/2 -translate-y-1/2 text-gray-500 hover:text-gray-300"
                  onClick={() => setShowPassword(!showPassword)}
                >
                  {showPassword ? <EyeOff className="w-5 h-5" /> : <Eye className="w-5 h-5" />}
                </button>
              </div>
              {errors.password && (
                <p className="mt-1 text-sm text-red-400">{errors.password.message}</p>
              )}
            </div>

            {/* MFA Code (shown if required) */}
            {mfaRequired && (
              <div>
                <label htmlFor="totp_code" className="block text-sm font-medium text-gray-300 mb-2">
                  <Lock className="w-4 h-4 inline mr-2" />
                  {t('auth.mfaCode')}
                </label>
                <input
                  id="totp_code"
                  type="text"
                  inputMode="numeric"
                  autoComplete="one-time-code"
                  className="input text-center text-2xl tracking-widest"
                  placeholder="000000"
                  maxLength={6}
                  {...register('totp_code', {
                    required: mfaRequired ? t('validation.required') : false,
                    pattern: {
                      value: /^\d{6}$/,
                      message: t('validation.required'),
                    },
                  })}
                />
                {errors.totp_code && (
                  <p className="mt-1 text-sm text-red-400">{errors.totp_code.message}</p>
                )}
              </div>
            )}

            {/* Submit button */}
            <button
              type="submit"
              disabled={loading}
              className="btn btn-primary w-full py-3"
            >
              {loading ? (
                <span className="flex items-center justify-center gap-2">
                  <svg className="animate-spin h-5 w-5" viewBox="0 0 24 24">
                    <circle
                      className="opacity-25"
                      cx="12"
                      cy="12"
                      r="10"
                      stroke="currentColor"
                      strokeWidth="4"
                      fill="none"
                    />
                    <path
                      className="opacity-75"
                      fill="currentColor"
                      d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
                    />
                  </svg>
                  {t('auth.loggingIn')}
                </span>
              ) : (
                t('auth.loginButton')
              )}
            </button>
          </form>
        </div>

        {/* Footer */}
        <p className="text-center text-gray-500 text-sm mt-8">
          XMPanel - Secure XMPP Server Management
        </p>
      </div>
    </div>
  )
}
