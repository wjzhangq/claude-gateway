import { useEffect, useState } from 'react'
import { listMyApplications, submitApplication } from '../api'

interface Application {
  id: number
  model: string
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
  pending: 'bg-yellow-50 text-yellow-700',
  approved: 'bg-green-50 text-green-700',
  rejected: 'bg-red-50 text-red-700',
}

export default function ApplicationsPage() {
  const [apps, setApps] = useState<Application[]>([])
  const [loading, setLoading] = useState(true)
  const [showForm, setShowForm] = useState(false)
  const [model, setModel] = useState('')
  const [reason, setReason] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')

  const load = () => {
    setLoading(true)
    listMyApplications()
      .then((res) => setApps(res.data.applications || []))
      .finally(() => setLoading(false))
  }

  useEffect(() => { load() }, [])

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!model || !reason) { setError('请填写模型和申请理由'); return }
    setSubmitting(true)
    setError('')
    try {
      await submitApplication(model, reason)
      setShowForm(false)
      setModel('')
      setReason('')
      load()
    } catch (e: unknown) {
      const msg = (e as { response?: { data?: { error?: string } } })?.response?.data?.error
      setError(msg || '提交失败')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="p-8">
      <div className="flex items-center justify-between mb-6">
        <h2 className="text-xl font-semibold text-gray-900">申请记录</h2>
        <button
          onClick={() => setShowForm(true)}
          className="px-4 py-2 bg-indigo-600 text-white text-sm rounded-md hover:bg-indigo-700"
        >
          提交申请
        </button>
      </div>

      {showForm && (
        <div className="mb-6 bg-white border border-gray-200 rounded-lg p-5">
          <h3 className="text-sm font-medium text-gray-700 mb-4">申请模型访问权限</h3>
          <form onSubmit={handleSubmit} className="space-y-3">
            <div>
              <label className="block text-sm text-gray-600 mb-1">模型名称</label>
              <input
                value={model}
                onChange={(e) => setModel(e.target.value)}
                placeholder="如 claude-opus-4-5"
                className="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500"
              />
            </div>
            <div>
              <label className="block text-sm text-gray-600 mb-1">申请理由</label>
              <textarea
                value={reason}
                onChange={(e) => setReason(e.target.value)}
                rows={3}
                placeholder="请说明使用场景和需求..."
                className="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500"
              />
            </div>
            {error && <p className="text-sm text-red-600">{error}</p>}
            <div className="flex gap-2">
              <button
                type="submit"
                disabled={submitting}
                className="px-4 py-2 bg-indigo-600 text-white text-sm rounded-md hover:bg-indigo-700 disabled:opacity-50"
              >
                {submitting ? '提交中...' : '提交'}
              </button>
              <button
                type="button"
                onClick={() => setShowForm(false)}
                className="px-4 py-2 text-sm border border-gray-300 rounded-md hover:bg-gray-50"
              >
                取消
              </button>
            </div>
          </form>
        </div>
      )}

      <div className="bg-white rounded-lg border border-gray-200">
        {loading ? (
          <div className="p-6 text-sm text-gray-400">加载中...</div>
        ) : apps.length === 0 ? (
          <div className="p-6 text-sm text-gray-400">暂无申请记录</div>
        ) : (
          <table className="w-full text-sm">
            <thead className="bg-gray-50">
              <tr>
                {['模型', '申请理由', '状态', '审批备注', '申请时间'].map((h) => (
                  <th key={h} className="px-4 py-3 text-left text-xs font-medium text-gray-500">
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100">
              {apps.map((app) => (
                <tr key={app.id}>
                  <td className="px-4 py-3 font-mono text-xs">{app.model}</td>
                  <td className="px-4 py-3 text-gray-600 max-w-xs truncate">{app.reason}</td>
                  <td className="px-4 py-3">
                    <span className={`px-2 py-0.5 rounded text-xs ${STATUS_CLASS[app.status] || ''}`}>
                      {STATUS_LABEL[app.status] || app.status}
                    </span>
                  </td>
                  <td className="px-4 py-3 text-gray-500 text-xs">{app.review_note || '-'}</td>
                  <td className="px-4 py-3 text-gray-400 text-xs">
                    {new Date(app.created_at).toLocaleDateString()}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  )
}
