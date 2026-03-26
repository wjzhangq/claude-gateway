import { useEffect, useState } from 'react'
import { useAuth } from '../context/AuthContext'
import { logout, listMyApplications, submitApplication } from '../api'
import { formatDate } from '../utils/time'

interface Application {
  id: number
  reason: string
  status: string
  review_note: string | null
  created_at: string
}

const STATUS_LABEL: Record<string, string> = {
  pending: '待审批',
  approved: '已通过',
  rejected: '已拒绝',
}

const STATUS_CLASS: Record<string, string> = {
  pending: 'bg-yellow-50 text-yellow-700 ring-yellow-100',
  approved: 'bg-green-50 text-green-700 ring-green-100',
  rejected: 'bg-red-50 text-red-700 ring-red-100',
}

export default function PendingPage() {
  const { user, setUser } = useAuth()
  const [apps, setApps] = useState<Application[]>([])
  const [loading, setLoading] = useState(true)
  const [reason, setReason] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')
  const [submitted, setSubmitted] = useState(false)

  const isPending = user?.status === 'pending'

  const load = () => {
    setLoading(true)
    listMyApplications()
      .then((res) => setApps(res.data.applications || []))
      .catch(() => {})
      .finally(() => setLoading(false))
  }

  useEffect(() => { load() }, [])

  const hasPendingApp = apps.some((a) => a.status === 'pending')

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!reason.trim()) { setError('请填写申请理由'); return }
    setSubmitting(true)
    setError('')
    try {
      await submitApplication(reason)
      setReason('')
      setSubmitted(true)
      load()
    } catch (e: unknown) {
      const msg = (e as { response?: { data?: { error?: string } } })?.response?.data?.error
      setError(msg || '提交失败')
    } finally {
      setSubmitting(false)
    }
  }

  const handleLogout = async () => {
    await logout()
    setUser(null)
    window.location.href = '/login'
  }

  return (
    <div className="min-h-screen bg-gray-50 flex flex-col items-center justify-start pt-20 px-4">
      <div className="w-full max-w-lg">
        {/* Header */}
        <div className="flex items-center justify-between mb-8">
          <div className="flex items-center gap-3">
            <div className="w-8 h-8 bg-red-600 rounded-lg flex items-center justify-center">
              <span className="text-white text-sm font-bold">C</span>
            </div>
            <span className="text-lg font-semibold text-gray-900">Claude Gateway</span>
          </div>
          <button
            onClick={handleLogout}
            className="text-sm text-gray-400 hover:text-gray-600 transition-colors"
          >
            退出登录
          </button>
        </div>

        {/* Status card */}
        <div className="bg-white rounded-2xl border border-gray-100 shadow-sm p-6 mb-6">
          <div className="flex items-start gap-4">
            <div className={`w-10 h-10 rounded-full flex items-center justify-center flex-shrink-0 ${
              isPending ? 'bg-yellow-100' : 'bg-red-100'
            }`}>
              <span className="text-lg">{isPending ? '⏳' : '🚫'}</span>
            </div>
            <div>
              <h2 className="text-base font-semibold text-gray-900 mb-1">
                {isPending ? '账号待审批' : '账号已停用'}
              </h2>
              <p className="text-sm text-gray-500">
                {isPending
                  ? '您的账号正在等待管理员审批，审批通过后即可正常使用。'
                  : '您的账号已被停用，如需恢复请提交申请。'}
              </p>
              <p className="text-xs text-gray-400 mt-1">当前账号：{user?.itcode}</p>
            </div>
          </div>
        </div>

        {/* Submit application */}
        {!hasPendingApp && !submitted && (
          <div className="bg-white rounded-2xl border border-gray-100 shadow-sm p-6 mb-6">
            <h3 className="text-sm font-semibold text-gray-700 mb-4">提交开通申请</h3>
            <form onSubmit={handleSubmit} className="space-y-3">
              <div>
                <label className="block text-xs font-semibold text-gray-500 mb-1.5 uppercase tracking-wide">
                  申请理由
                </label>
                <textarea
                  value={reason}
                  onChange={(e) => setReason(e.target.value)}
                  rows={3}
                  placeholder="请说明您的使用场景和需求..."
                  className="w-full px-3.5 py-2.5 border border-gray-200 rounded-xl text-sm bg-gray-50 focus:bg-white focus:outline-none focus:ring-2 focus:ring-red-500/30 focus:border-red-400 transition-all resize-none"
                />
              </div>
              {error && <p className="text-sm text-red-600">{error}</p>}
              <button
                type="submit"
                disabled={submitting}
                className="w-full py-2.5 bg-red-600 text-white text-sm font-medium rounded-xl hover:bg-red-700 disabled:opacity-50 transition-colors"
              >
                {submitting ? '提交中...' : '提交申请'}
              </button>
            </form>
          </div>
        )}

        {submitted && (
          <div className="bg-green-50 border border-green-100 rounded-2xl p-4 mb-6 text-sm text-green-700">
            申请已提交，请等待管理员审批。
          </div>
        )}

        {hasPendingApp && !submitted && (
          <div className="bg-yellow-50 border border-yellow-100 rounded-2xl p-4 mb-6 text-sm text-yellow-700">
            您已有一条待审批的申请，请耐心等待管理员处理。
          </div>
        )}

        {/* Application history */}
        {!loading && apps.length > 0 && (
          <div className="bg-white rounded-2xl border border-gray-100 shadow-sm overflow-hidden">
            <div className="px-5 py-4 border-b border-gray-50">
              <h3 className="text-sm font-semibold text-gray-700">申请记录</h3>
            </div>
            <div className="divide-y divide-gray-50">
              {apps.map((app) => (
                <div key={app.id} className="px-5 py-4">
                  <div className="flex items-start justify-between gap-3">
                    <p className="text-sm text-gray-600 flex-1">{app.reason}</p>
                    <span className={`inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium ring-1 flex-shrink-0 ${STATUS_CLASS[app.status] || ''}`}>
                      {STATUS_LABEL[app.status] || app.status}
                    </span>
                  </div>
                  {app.review_note && (
                    <p className="text-xs text-gray-400 mt-1.5">备注：{app.review_note}</p>
                  )}
                  <p className="text-xs text-gray-300 mt-1">{formatDate(app.created_at)}</p>
                </div>
              ))}
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
