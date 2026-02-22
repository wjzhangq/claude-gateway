import { useEffect, useState } from 'react'
import { adminListApplications, adminReviewApplication } from '../api'

interface Application {
  id: number
  user_id: number
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

export default function AdminApplicationsPage() {
  const [apps, setApps] = useState<Application[]>([])
  const [loading, setLoading] = useState(true)
  const [filter, setFilter] = useState('pending')
  const [reviewId, setReviewId] = useState<number | null>(null)
  const [note, setNote] = useState('')
  const [submitting, setSubmitting] = useState(false)

  const load = (status: string) => {
    setLoading(true)
    adminListApplications(status)
      .then((res) => setApps(res.data.applications || []))
      .finally(() => setLoading(false))
  }

  useEffect(() => { load(filter) }, [filter])

  const handleReview = async (id: number, status: 'approved' | 'rejected') => {
    setSubmitting(true)
    try {
      await adminReviewApplication(id, status, note)
      setReviewId(null)
      setNote('')
      load(filter)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="p-8">
      <h2 className="text-xl font-semibold text-gray-900 mb-6">审批管理</h2>

      <div className="flex gap-2 mb-6">
        {['pending', 'approved', 'rejected'].map((s) => (
          <button
            key={s}
            onClick={() => setFilter(s)}
            className={`px-4 py-1.5 text-sm rounded-full border transition-colors ${
              filter === s
                ? 'bg-indigo-600 text-white border-indigo-600'
                : 'border-gray-300 text-gray-600 hover:bg-gray-50'
            }`}
          >
            {STATUS_LABEL[s]}
          </button>
        ))}
      </div>

      <div className="bg-white rounded-lg border border-gray-200">
        {loading ? (
          <div className="p-6 text-sm text-gray-400">加载中...</div>
        ) : apps.length === 0 ? (
          <div className="p-6 text-sm text-gray-400">暂无数据</div>
        ) : (
          <table className="w-full text-sm">
            <thead className="bg-gray-50">
              <tr>
                {['用户ID', '模型', '申请理由', '状态', '审批备注', '申请时间', '操作'].map((h) => (
                  <th key={h} className="px-4 py-3 text-left text-xs font-medium text-gray-500">
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100">
              {apps.map((app) => (
                <>
                  <tr key={app.id}>
                    <td className="px-4 py-3 text-gray-500">{app.user_id}</td>
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
                    <td className="px-4 py-3">
                      {app.status === 'pending' && (
                        <button
                          onClick={() => setReviewId(reviewId === app.id ? null : app.id)}
                          className="text-xs text-indigo-600 hover:underline"
                        >
                          审批
                        </button>
                      )}
                    </td>
                  </tr>
                  {reviewId === app.id && (
                    <tr key={`review-${app.id}`}>
                      <td colSpan={7} className="px-4 py-3 bg-gray-50">
                        <div className="flex items-center gap-3">
                          <input
                            value={note}
                            onChange={(e) => setNote(e.target.value)}
                            placeholder="审批备注（可选）"
                            className="flex-1 px-3 py-1.5 border border-gray-300 rounded text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500"
                          />
                          <button
                            onClick={() => handleReview(app.id, 'approved')}
                            disabled={submitting}
                            className="px-3 py-1.5 bg-green-600 text-white text-sm rounded hover:bg-green-700 disabled:opacity-50"
                          >
                            通过
                          </button>
                          <button
                            onClick={() => handleReview(app.id, 'rejected')}
                            disabled={submitting}
                            className="px-3 py-1.5 bg-red-500 text-white text-sm rounded hover:bg-red-600 disabled:opacity-50"
                          >
                            拒绝
                          </button>
                          <button
                            onClick={() => setReviewId(null)}
                            className="px-3 py-1.5 text-sm border border-gray-300 rounded hover:bg-gray-100"
                          >
                            取消
                          </button>
                        </div>
                      </td>
                    </tr>
                  )}
                </>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  )
}
