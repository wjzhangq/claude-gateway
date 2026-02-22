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
  pending: 'bg-yellow-50 text-yellow-700 ring-yellow-100',
  approved: 'bg-green-50 text-green-700 ring-green-100',
  rejected: 'bg-red-50 text-red-700 ring-red-100',
}

function SkeletonRow() {
  return (
    <tr>
      {[60, 130, 180, 70, 100, 90, 60].map((w, i) => (
        <td key={i} className="px-4 py-3.5">
          <div className="skeleton h-3.5 rounded" style={{ width: w }} />
        </td>
      ))}
    </tr>
  )
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
      <div className="mb-7">
        <h2 className="text-xl font-bold text-gray-900">审批管理</h2>
        <p className="text-sm text-gray-400 mt-0.5">审核用户的模型访问申请</p>
      </div>

      <div className="flex gap-2 mb-6">
        {['pending', 'approved', 'rejected'].map((s) => (
          <button
            key={s}
            onClick={() => setFilter(s)}
            className={`px-4 py-1.5 text-sm rounded-full font-medium transition-all ${
              filter === s
                ? 'bg-red-600 text-white shadow-sm'
                : 'bg-white border border-gray-200 text-gray-500 hover:bg-gray-50 hover:text-gray-700'
            }`}
          >
            {STATUS_LABEL[s]}
          </button>
        ))}
      </div>

      <div className="bg-white rounded-xl border border-gray-100 shadow-sm overflow-hidden">
        <table className="w-full text-sm">
          <thead className="bg-gray-50/80">
            <tr>
              {['用户ID', '模型', '申请理由', '状态', '审批备注', '申请时间', '操作'].map((h) => (
                <th key={h} className="px-4 py-3 text-left text-xs font-semibold text-gray-400 uppercase tracking-wide">
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-50">
            {loading ? (
              Array.from({ length: 4 }).map((_, i) => <SkeletonRow key={i} />)
            ) : apps.length === 0 ? (
              <tr>
                <td colSpan={7} className="px-4 py-10 text-center text-sm text-gray-400">暂无数据</td>
              </tr>
            ) : (
              apps.map((app) => (
                <>
                  <tr key={app.id} className="hover:bg-gray-50/50 transition-colors">
                    <td className="px-4 py-3.5 text-gray-500 text-xs">{app.user_id}</td>
                    <td className="px-4 py-3.5 font-mono text-xs text-gray-700">{app.model}</td>
                    <td className="px-4 py-3.5 text-gray-500 max-w-xs truncate">{app.reason}</td>
                    <td className="px-4 py-3.5">
                      <span className={`inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium ring-1 ${STATUS_CLASS[app.status] || ''}`}>
                        {STATUS_LABEL[app.status] || app.status}
                      </span>
                    </td>
                    <td className="px-4 py-3.5 text-gray-400 text-xs">{app.review_note || '—'}</td>
                    <td className="px-4 py-3.5 text-gray-400 text-xs">
                      {new Date(app.created_at).toLocaleDateString()}
                    </td>
                    <td className="px-4 py-3.5">
                      {app.status === 'pending' && (
                        <button
                          onClick={() => setReviewId(reviewId === app.id ? null : app.id)}
                          className="text-xs text-red-500 hover:text-red-700 font-medium transition-colors"
                        >
                          审批
                        </button>
                      )}
                    </td>
                  </tr>
                  {reviewId === app.id && (
                    <tr key={`review-${app.id}`}>
                      <td colSpan={7} className="px-4 py-3 bg-blue-50/50 border-l-2 border-blue-300">
                        <div className="flex items-center gap-3">
                          <input
                            value={note}
                            onChange={(e) => setNote(e.target.value)}
                            placeholder="审批备注（可选）"
                            className="flex-1 px-3.5 py-2 border border-gray-200 rounded-xl text-sm bg-white focus:outline-none focus:ring-2 focus:ring-red-500/30 focus:border-red-400 transition-all"
                          />
                          <button
                            onClick={() => handleReview(app.id, 'approved')}
                            disabled={submitting}
                            className="px-4 py-2 bg-green-600 text-white text-sm font-medium rounded-xl hover:bg-green-700 disabled:opacity-50 transition-colors"
                          >
                            通过
                          </button>
                          <button
                            onClick={() => handleReview(app.id, 'rejected')}
                            disabled={submitting}
                            className="px-4 py-2 bg-red-500 text-white text-sm font-medium rounded-xl hover:bg-red-600 disabled:opacity-50 transition-colors"
                          >
                            拒绝
                          </button>
                          <button
                            onClick={() => setReviewId(null)}
                            className="px-4 py-2 text-sm border border-gray-200 rounded-xl hover:bg-gray-100 transition-colors"
                          >
                            取消
                          </button>
                        </div>
                      </td>
                    </tr>
                  )}
                </>
              ))
            )}
          </tbody>
        </table>
      </div>
    </div>
  )
}
