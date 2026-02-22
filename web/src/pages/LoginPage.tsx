import { useState, useRef } from 'react'
import { useNavigate } from 'react-router-dom'
import { sendCode, login } from '../api'
import { useAuth } from '../context/AuthContext'

export default function LoginPage() {
  const [phone, setPhone] = useState('')
  const [code, setCode] = useState('')
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
    if (!phone) { setError('请输入手机号'); return }
    setError('')
    try {
      await sendCode(phone)
      startCountdown()
    } catch (e: unknown) {
      const msg = (e as { response?: { data?: { error?: string } } })?.response?.data?.error
      setError(msg || '发送失败，请重试')
    }
  }

  const handleLogin = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!phone || !code) { setError('请填写手机号和验证码'); return }
    setError('')
    setLoading(true)
    try {
      const res = await login(phone, code)
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
    <div className="min-h-screen flex items-center justify-center bg-gray-50">
      <div className="w-full max-w-sm bg-white rounded-xl shadow-sm border border-gray-200 p-8">
        <h2 className="text-2xl font-bold text-gray-900 mb-2">Claude Gateway</h2>
        <p className="text-sm text-gray-500 mb-6">使用手机号验证码登录</p>

        {error && (
          <div className="mb-4 px-3 py-2 bg-red-50 border border-red-200 rounded-md text-sm text-red-600">
            {error}
          </div>
        )}

        <form onSubmit={handleLogin} className="space-y-4">
          <div>
            <label className="block text-sm font-medium text-gray-700 mb-1">手机号</label>
            <input
              type="tel"
              value={phone}
              onChange={(e) => setPhone(e.target.value)}
              placeholder="请输入手机号"
              className="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500"
            />
          </div>

          <div>
            <label className="block text-sm font-medium text-gray-700 mb-1">验证码</label>
            <div className="flex gap-2">
              <input
                type="text"
                value={code}
                onChange={(e) => setCode(e.target.value)}
                placeholder="6位验证码"
                maxLength={6}
                className="flex-1 px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500"
              />
              <button
                type="button"
                onClick={handleSendCode}
                disabled={countdown > 0}
                className="px-3 py-2 text-sm bg-indigo-50 text-indigo-600 border border-indigo-200 rounded-md hover:bg-indigo-100 disabled:opacity-50 disabled:cursor-not-allowed whitespace-nowrap"
              >
                {countdown > 0 ? `${countdown}s` : '发送验证码'}
              </button>
            </div>
          </div>

          <button
            type="submit"
            disabled={loading}
            className="w-full py-2 px-4 bg-indigo-600 text-white text-sm font-medium rounded-md hover:bg-indigo-700 disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {loading ? '登录中...' : '登录'}
          </button>
        </form>
      </div>
    </div>
  )
}
