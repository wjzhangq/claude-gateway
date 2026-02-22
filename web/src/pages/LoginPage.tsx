import { useState, useRef } from 'react'
import { useNavigate } from 'react-router-dom'
import { sendCode, login } from '../api'
import { useAuth } from '../context/AuthContext'

export default function LoginPage() {
  const [itcode, setItcode] = useState('')
  const [code, setCode] = useState('')
  const [inviteCode, setInviteCode] = useState('')
  const [countdown, setCountdown] = useState(0)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const timerRef = useRef<ReturnType<typeof setInterval> | null>(null)
  const { setUser } = useAuth()
  const navigate = useNavigate()

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

  const handleSendCode = async () => {
    if (!itcode) { setError('请输入 itcode'); return }
    if (!inviteCode) { setError('请输入邀请码'); return }
    setError('')
    try {
      await sendCode(itcode, inviteCode)
      startCountdown()
    } catch (e: unknown) {
      const msg = (e as { response?: { data?: { error?: string } } })?.response?.data?.error
      setError(msg || '发送失败，请重试')
    }
  }

  const handleLogin = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!itcode || !code) { setError('请填写 itcode 和验证码'); return }
    if (!inviteCode) { setError('请输入邀请码'); return }
    setError('')
    setLoading(true)
    try {
      const res = await login(itcode, code, inviteCode)
      setUser(res.data.user)
      navigate('/dashboard')
    } catch (e: unknown) {
      const msg = (e as { response?: { data?: { error?: string } } })?.response?.data?.error
      setError(msg || '登录失败，请重试')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center bg-gradient-to-br from-gray-50 via-red-50/20 to-gray-100">
      <div className="w-full max-w-sm">
        {/* Card */}
        <div className="bg-white rounded-2xl shadow-lg border border-gray-100 overflow-hidden">
          {/* Header strip */}
          <div className="h-1.5 bg-gradient-to-r from-red-500 via-red-600 to-red-700" />

          <div className="px-8 py-8">
            {/* Logo */}
            <div className="flex items-center gap-3 mb-7">
              <div className="w-9 h-9 bg-red-600 rounded-xl flex items-center justify-center shadow-sm">
                <span className="text-white text-sm font-bold">CG</span>
              </div>
              <div>
                <h1 className="text-base font-bold text-gray-900 leading-tight">Claude Gateway</h1>
                <p className="text-xs text-gray-400">使用 itcode 验证码登录</p>
              </div>
            </div>

            {error && (
              <div className="mb-5 px-3.5 py-2.5 bg-red-50 border border-red-100 rounded-xl text-sm text-red-600 flex items-start gap-2">
                <span className="mt-0.5 flex-shrink-0">⚠</span>
                <span>{error}</span>
              </div>
            )}

            <form onSubmit={handleLogin} className="space-y-4">
              <div>
                <label className="block text-xs font-semibold text-gray-600 mb-1.5 uppercase tracking-wide">Itcode</label>
                <input
                  type="text"
                  value={itcode}
                  onChange={(e) => setItcode(e.target.value)}
                  placeholder="请输入 itcode"
                  className="w-full px-3.5 py-2.5 border border-gray-200 rounded-xl text-sm bg-gray-50 focus:bg-white focus:outline-none focus:ring-2 focus:ring-red-500/30 focus:border-red-400 transition-all"
                />
              </div>

              <div>
                <label className="block text-xs font-semibold text-gray-600 mb-1.5 uppercase tracking-wide">邀请码</label>
                <input
                  type="text"
                  value={inviteCode}
                  onChange={(e) => setInviteCode(e.target.value)}
                  placeholder="请输入邀请码"
                  className="w-full px-3.5 py-2.5 border border-gray-200 rounded-xl text-sm bg-gray-50 focus:bg-white focus:outline-none focus:ring-2 focus:ring-red-500/30 focus:border-red-400 transition-all"
                />
              </div>

              <div>
                <label className="block text-xs font-semibold text-gray-600 mb-1.5 uppercase tracking-wide">验证码</label>
                <div className="flex gap-2">
                  <input
                    type="text"
                    value={code}
                    onChange={(e) => setCode(e.target.value)}
                    placeholder="6位验证码"
                    maxLength={6}
                    className="flex-1 px-3.5 py-2.5 border border-gray-200 rounded-xl text-sm bg-gray-50 focus:bg-white focus:outline-none focus:ring-2 focus:ring-red-500/30 focus:border-red-400 transition-all"
                  />
                  <button
                    type="button"
                    onClick={handleSendCode}
                    disabled={countdown > 0}
                    className="px-3.5 py-2.5 text-sm bg-red-50 text-red-600 border border-red-100 rounded-xl hover:bg-red-100 disabled:opacity-50 disabled:cursor-not-allowed whitespace-nowrap font-medium transition-colors"
                  >
                    {countdown > 0 ? `${countdown}s` : '发送验证码'}
                  </button>
                </div>
              </div>

              <button
                type="submit"
                disabled={loading}
                className="w-full py-2.5 px-4 bg-red-600 text-white text-sm font-semibold rounded-xl hover:bg-red-700 disabled:opacity-50 disabled:cursor-not-allowed shadow-sm hover:shadow-md transition-all mt-2"
              >
                {loading ? '登录中...' : '登录'}
              </button>
            </form>
          </div>
        </div>

        <p className="text-center text-xs text-gray-400 mt-5">Claude Gateway · 内部使用</p>
      </div>
    </div>
  )
}
