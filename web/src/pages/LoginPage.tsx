import { useState, useRef, useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import { checkEmail, sendCode, login, resetPassword } from '../api'
import { useAuth } from '../context/AuthContext'

const REDIRECT_KEY = 'login_redirect_count'
const REDIRECT_WINDOW_MS = 5 * 60 * 1000
const SAVED_EMAIL_KEY = 'saved_email'

export default function LoginPage() {
  const [email, setEmail] = useState(() => localStorage.getItem(SAVED_EMAIL_KEY) || '')
  const [password, setPassword] = useState('')
  const [rememberMe, setRememberMe] = useState(true)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')

  // Email check state
  const [emailStatus, setEmailStatus] = useState<'unknown' | 'checking' | 'ok' | 'not_found' | 'no_password'>('unknown')
  const [emailError, setEmailError] = useState('')

  // Password reset modal state
  const [showResetModal, setShowResetModal] = useState(false)
  const [resetEmail, setResetEmail] = useState('')
  const [resetCode, setResetCode] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [resetStep, setResetStep] = useState<'email' | 'code'>('email')
  const [countdown, setCountdown] = useState(0)
  const [resetLoading, setResetLoading] = useState(false)
  const [resetError, setResetError] = useState('')
  const timerRef = useRef<ReturnType<typeof setInterval> | null>(null)

  const { user, setUser } = useAuth()
  const navigate = useNavigate()

  // Check redirect count to prevent loops
  const canRedirect = (): boolean => {
    try {
      const stored = localStorage.getItem(REDIRECT_KEY)
      const now = Date.now()

      if (!stored) {
        localStorage.setItem(REDIRECT_KEY, JSON.stringify({ count: 1, timestamp: now }))
        return true
      }

      const data = JSON.parse(stored) as { count: number; timestamp: number }

      if (now - data.timestamp > REDIRECT_WINDOW_MS) {
        localStorage.setItem(REDIRECT_KEY, JSON.stringify({ count: 1, timestamp: now }))
        return true
      }

      if (data.count >= 3) {
        console.warn('[LoginPage] Redirect loop detected, blocking auto-redirect')
        return false
      }

      localStorage.setItem(REDIRECT_KEY, JSON.stringify({ count: data.count + 1, timestamp: data.timestamp }))
      return true
    } catch (e) {
      console.error('[LoginPage] Failed to check redirect count:', e)
      return true
    }
  }

  // Redirect if already logged in
  useEffect(() => {
    if (user && canRedirect()) {
      navigate('/dashboard', { replace: true })
    }
  }, [user, navigate])

  const startCountdown = () => {
    setCountdown(60)
    timerRef.current = setInterval(() => {
      setCountdown((c) => {
        if (c <= 1) {
          clearInterval(timerRef.current!)
          return 0
        }
        return c - 1
      })
    }, 1000)
  }

  const handleEmailBlur = async () => {
    const trimmed = email.trim()
    if (!trimmed) {
      setEmailStatus('unknown')
      return
    }

    if (!trimmed.includes('@') || trimmed.split('@').length !== 2) {
      setEmailError('Please enter a valid email address')
      setEmailStatus('unknown')
      return
    }

    // Save email to localStorage
    localStorage.setItem(SAVED_EMAIL_KEY, trimmed)

    // Check email existence and password status
    setEmailStatus('checking')
    setEmailError('')
    try {
      const res = await checkEmail(trimmed)
      const { exists, password_set } = res.data

      if (!exists) {
        setEmailStatus('not_found')
        setEmailError('User not found. Please contact your administrator.')
      } else if (!password_set) {
        setEmailStatus('no_password')
        setEmailError('')
      } else {
        setEmailStatus('ok')
        setEmailError('')
      }
    } catch (e: unknown) {
      console.error('Email check failed:', e)
      setEmailStatus('unknown')
      setEmailError('')
    }
  }

  const handleLogin = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!email || !password) {
      setError('Please enter email and password')
      return
    }
    if (emailStatus === 'not_found') {
      setError('User not found. Please contact your administrator.')
      return
    }
    setError('')
    setLoading(true)
    try {
      const res = await login(email, password, rememberMe)
      setUser(res.data.user)
      navigate('/dashboard')
    } catch (e: unknown) {
      const err = e as { response?: { data?: { error?: string; message?: string } } }
      const errorCode = err?.response?.data?.error
      const errorMsg = err?.response?.data?.message || errorCode

      if (errorCode === 'password_not_set') {
        setError(errorMsg || 'Please set your password first')
        setEmailStatus('no_password')
      } else {
        setError(errorMsg || 'Login failed, please try again')
      }
    } finally {
      setLoading(false)
    }
  }

  const handleSetPassword = () => {
    setResetEmail(email)
    setShowResetModal(true)
    setResetStep('email')
  }

  const handleSendResetCode = async () => {
    if (!resetEmail) {
      setResetError('Please enter your email address')
      return
    }
    if (!resetEmail.includes('@')) {
      setResetError('Please enter a valid email address')
      return
    }
    setResetError('')
    setResetLoading(true)
    try {
      await sendCode(resetEmail)
      setResetStep('code')
      startCountdown()
    } catch (e: unknown) {
      const msg = (e as { response?: { data?: { error?: string } } })?.response?.data?.error
      setResetError(msg || 'Failed to send code')
    } finally {
      setResetLoading(false)
    }
  }

  const handleResetPassword = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!resetCode || !newPassword) {
      setResetError('Please enter code and new password')
      return
    }
    setResetError('')
    setResetLoading(true)
    try {
      await resetPassword(resetEmail, resetCode, newPassword)
      setShowResetModal(false)
      setResetStep('email')
      setResetEmail('')
      setResetCode('')
      setNewPassword('')
      setError('Password set successfully! Please log in.')
      setEmailStatus('ok')
    } catch (e: unknown) {
      const msg = (e as { response?: { data?: { error?: string } } })?.response?.data?.error
      setResetError(msg || 'Failed to reset password')
    } finally {
      setResetLoading(false)
    }
  }

  const handleCloseResetModal = () => {
    setShowResetModal(false)
    setResetStep('email')
    setResetEmail('')
    setResetCode('')
    setNewPassword('')
    setResetError('')
    setCountdown(0)
    if (timerRef.current) {
      clearInterval(timerRef.current)
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center bg-gradient-to-br from-gray-50 via-red-50/20 to-gray-100">
      <div className="w-full max-w-sm">
        <div className="bg-white rounded-2xl shadow-lg border border-gray-100 overflow-hidden">
          <div className="h-1.5 bg-gradient-to-r from-red-500 via-red-600 to-red-700" />

          <div className="px-8 py-8">
            <div className="flex items-center gap-3 mb-7">
              <div className="w-9 h-9 bg-red-600 rounded-xl flex items-center justify-center shadow-sm">
                <span className="text-white text-sm font-bold">CG</span>
              </div>
              <div>
                <h1 className="text-base font-bold text-gray-900 leading-tight">Claude Gateway</h1>
                <p className="text-xs text-gray-400">Email + Password Login</p>
              </div>
            </div>

            <div className="mb-5 px-3.5 py-2.5 bg-yellow-50 border border-yellow-100 rounded-xl text-sm text-yellow-700 flex items-start gap-2">
              <span className="mt-0.5 flex-shrink-0">ℹ️</span>
              <span>New user registration is not available. Please contact your administrator.</span>
            </div>

            {error && (
              <div className="mb-5 px-3.5 py-2.5 bg-red-50 border border-red-100 rounded-xl text-sm text-red-600 flex items-start gap-2">
                <span className="mt-0.5 flex-shrink-0">⚠</span>
                <span>{error}</span>
              </div>
            )}

            {emailError && (
              <div className="mb-5 px-3.5 py-2.5 bg-red-50 border border-red-100 rounded-xl text-sm text-red-600 flex items-start gap-2">
                <span className="mt-0.5 flex-shrink-0">⚠</span>
                <span>{emailError}</span>
              </div>
            )}

            {emailStatus === 'no_password' && (
              <div className="mb-5 px-3.5 py-2.5 bg-blue-50 border border-blue-100 rounded-xl text-sm text-blue-700">
                <p className="font-medium mb-2">Password not set</p>
                <p className="text-xs mb-3">You need to set your password before logging in.</p>
                <button
                  onClick={handleSetPassword}
                  className="w-full py-2 px-3 bg-blue-600 text-white text-sm font-semibold rounded-lg hover:bg-blue-700"
                >
                  Set Password Now
                </button>
              </div>
            )}

            <form onSubmit={handleLogin} className="space-y-4">
              <div>
                <label className="block text-xs font-semibold text-gray-600 mb-1.5 uppercase tracking-wide">
                  Email
                  {emailStatus === 'checking' && <span className="ml-2 text-gray-400 font-normal">Checking...</span>}
                  {emailStatus === 'ok' && <span className="ml-2 text-green-600 font-normal">✓</span>}
                </label>
                <input
                  type="email"
                  value={email}
                  onChange={(e) => {
                    setEmail(e.target.value)
                    setEmailStatus('unknown')
                    setEmailError('')
                    setError('')
                  }}
                  onBlur={handleEmailBlur}
                  placeholder="user@example.com"
                  className="w-full px-3.5 py-2.5 border border-gray-200 rounded-xl text-sm bg-gray-50 focus:bg-white focus:outline-none focus:ring-2 focus:ring-red-500/30 focus:border-red-400 transition-all"
                />
              </div>

              {emailStatus !== 'no_password' && (
                <div>
                  <label className="block text-xs font-semibold text-gray-600 mb-1.5 uppercase tracking-wide">Password</label>
                  <input
                    type="password"
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                    placeholder="Enter password"
                    disabled={emailStatus === 'not_found'}
                    className="w-full px-3.5 py-2.5 border border-gray-200 rounded-xl text-sm bg-gray-50 focus:bg-white focus:outline-none focus:ring-2 focus:ring-red-500/30 focus:border-red-400 transition-all disabled:opacity-50 disabled:cursor-not-allowed"
                  />
                </div>
              )}

              {emailStatus !== 'no_password' && emailStatus !== 'not_found' && (
                <>
                  <div className="flex items-center justify-between">
                    <div className="flex items-center gap-2">
                      <input
                        id="remember-me"
                        type="checkbox"
                        checked={rememberMe}
                        onChange={(e) => setRememberMe(e.target.checked)}
                        className="w-4 h-4 rounded border-gray-300 text-red-600 focus:ring-red-500/30 cursor-pointer"
                      />
                      <label htmlFor="remember-me" className="text-sm text-gray-500 cursor-pointer select-none">
                        Keep me logged in for 1 week
                      </label>
                    </div>
                  </div>

                  <button
                    type="submit"
                    disabled={loading}
                    className="w-full py-2.5 px-4 bg-red-600 text-white text-sm font-semibold rounded-xl hover:bg-red-700 disabled:opacity-50 disabled:cursor-not-allowed shadow-sm hover:shadow-md transition-all"
                  >
                    {loading ? 'Logging in...' : 'Login'}
                  </button>

                  <div className="text-center">
                    <button
                      type="button"
                      onClick={() => {
                        setResetEmail(email)
                        setShowResetModal(true)
                      }}
                      className="text-sm text-red-600 hover:text-red-700 font-medium"
                    >
                      Forgot/Reset Password?
                    </button>
                  </div>
                </>
              )}
            </form>
          </div>
        </div>

        <p className="text-center text-xs text-gray-400 mt-5">Claude Gateway · Internal Use Only</p>
      </div>

      {showResetModal && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50" onClick={handleCloseResetModal}>
          <div className="bg-white rounded-2xl shadow-2xl w-full max-w-md mx-4" onClick={(e) => e.stopPropagation()}>
            <div className="px-6 py-5 border-b border-gray-100">
              <h2 className="text-lg font-bold text-gray-900">Reset Password</h2>
              <p className="text-sm text-gray-500 mt-1">
                {resetStep === 'email'
                  ? 'Enter your email to receive a verification code'
                  : 'Enter the code and set a new password'}
              </p>
            </div>

            {resetError && (
              <div className="mx-6 mt-4 px-3.5 py-2.5 bg-red-50 border border-red-100 rounded-xl text-sm text-red-600">
                {resetError}
              </div>
            )}

            <div className="px-6 py-5">
              {resetStep === 'email' ? (
                <div className="space-y-4">
                  <div>
                    <label className="block text-sm font-medium text-gray-700 mb-2">Email Address</label>
                    <input
                      type="email"
                      value={resetEmail}
                      onChange={(e) => setResetEmail(e.target.value)}
                      placeholder="user@example.com"
                      className="w-full px-3.5 py-2.5 border border-gray-200 rounded-xl text-sm bg-gray-50 focus:bg-white focus:outline-none focus:ring-2 focus:ring-red-500/30 focus:border-red-400"
                    />
                  </div>
                  <button
                    type="button"
                    onClick={handleSendResetCode}
                    disabled={resetLoading}
                    className="w-full py-2.5 px-4 bg-red-600 text-white text-sm font-semibold rounded-xl hover:bg-red-700 disabled:opacity-50 disabled:cursor-not-allowed"
                  >
                    {resetLoading ? 'Sending...' : 'Send Verification Code'}
                  </button>
                </div>
              ) : (
                <form onSubmit={handleResetPassword} className="space-y-4">
                  <div>
                    <label className="block text-sm font-medium text-gray-700 mb-2">Verification Code</label>
                    <input
                      type="text"
                      value={resetCode}
                      onChange={(e) => setResetCode(e.target.value)}
                      placeholder="6-digit code"
                      maxLength={6}
                      className="w-full px-3.5 py-2.5 border border-gray-200 rounded-xl text-sm bg-gray-50 focus:bg-white focus:outline-none focus:ring-2 focus:ring-red-500/30 focus:border-red-400"
                    />
                  </div>
                  <div>
                    <label className="block text-sm font-medium text-gray-700 mb-2">New Password</label>
                    <input
                      type="password"
                      value={newPassword}
                      onChange={(e) => setNewPassword(e.target.value)}
                      placeholder="Min 8 chars, letter + digit"
                      className="w-full px-3.5 py-2.5 border border-gray-200 rounded-xl text-sm bg-gray-50 focus:bg-white focus:outline-none focus:ring-2 focus:ring-red-500/30 focus:border-red-400"
                    />
                    <p className="text-xs text-gray-500 mt-1">At least 8 characters with a letter and a digit</p>
                  </div>
                  <div className="flex gap-3">
                    <button
                      type="button"
                      onClick={handleSendResetCode}
                      disabled={countdown > 0 || resetLoading}
                      className="flex-1 py-2.5 px-4 bg-gray-100 text-gray-700 text-sm font-medium rounded-xl hover:bg-gray-200 disabled:opacity-50 disabled:cursor-not-allowed"
                    >
                      {countdown > 0 ? `Resend (${countdown}s)` : 'Resend Code'}
                    </button>
                    <button
                      type="submit"
                      disabled={resetLoading}
                      className="flex-1 py-2.5 px-4 bg-red-600 text-white text-sm font-semibold rounded-xl hover:bg-red-700 disabled:opacity-50 disabled:cursor-not-allowed"
                    >
                      {resetLoading ? 'Resetting...' : 'Reset Password'}
                    </button>
                  </div>
                </form>
              )}
            </div>

            <div className="px-6 py-4 bg-gray-50 border-t border-gray-100">
              <button
                type="button"
                onClick={handleCloseResetModal}
                className="w-full py-2 px-4 text-sm text-gray-600 hover:text-gray-800 font-medium"
              >
                Cancel
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
