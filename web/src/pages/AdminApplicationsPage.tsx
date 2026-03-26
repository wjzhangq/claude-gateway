import { useEffect, useState } from 'react'
import { adminListApplications, adminReviewApplication, adminGetGroups } from '../api'
import { formatDate } from '../utils/time'

interface Application {
  id: number
  user_id: number
  user_itcode: string
  user_name: string
  user_status: string
  group_id: number
  reason: string
  status: string
  review_note: string | null
  created_at: string
}

interface Group {
  id: number
  name: string
}

const APP_STATUS_LABEL: Record<string, string> = {
  pending: '待审批',
  approved: '已通过',
  rejected: '已拒绝',
}

const APP_STATUS_CLASS: Record<string, string> = {
  pending: 'bg-yellow-50 text-yellow-700 ring-yellow-100',
  approved: 'bg-green-50 text-green-700 ring-green-100',
  rejected: 'bg-red-50 text-red-700 ring-red-100',
}

const USER_STATUS_LABEL: Record<string, string> = {
  pending: '待激活',
  active: '已激活',
  disabled: '已停用',
}

const USER_STATUS_CLASS: Record<string, string> = {
  pending: 'bg-yellow-50 text-yellow-600 ring-yellow-100',
  active: 'bg-green-50 text-green-600 ring-green-100',
  disabled: 'bg-red-50 text-red-600 ring-red-100',
}

function SkeletonRow() {
  return (
    <tr>
      {[60, 130, 80, 180, 70, 100, 90, 60].map((w, i) => (
        <td key={i} className="px-4 py-3.5">
          <div className="skeleton h-3.5 rounded" style={{ width: w }} />
        </td>
      ))}
    </tr>
  )
}

export default function AdminApplicationsPage() {
  const [apps, setApps] = useState<Application[]>([])
  const [groups, setGroups] = useState<Group[]>([])
  const [loading, setLoading] = useState(true)
  const [filter, setFilter] = useState('pending')
  const [reviewId, setReviewId] = useState<number | null>(null)
  const [note, setNote] = useState('')
  const [selectedGroup, setSelectedGroup] = useState<number | null>(null)
  const [submitting, setSubmitting] = useState(false)

  const load = (status: string) => {
    setLoading(true)
    adminListApplications(status)
      .then((res) => setApps(res.data.applications || []))
      .catch(() => {})
      .finally(() => setLoading(false))
  }

  useEffect(() => {
    load(filter)
    adminGetGroups().then((res) => setGroups(res.data.groups || [])).catch(() => {})
  }, [filter])

  const handleReview = async (id: number, status: 'approved' | 'rejected') => {
    setSubmitting(true)
    try {
      await adminReviewApplication(id, status, note, status === 'approved' && selectedGroup ? selectedGroup : undefined)
      setReviewId(null)
      setNote('')
      setSelectedGroup(null)
      load(filter)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="p-8">
      <div className="mb-7">
        <h2 className="text-xl font-bold text-gray-900">账号审批</h2>
        <p className="text-sm text-gray-400 mt-0.5">审核用户的账号开通申请，通过后账号自动激活</p>
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
            {APP_STATUS_LABEL[s]}
          </button>
        ))}
      </div>

      <div className="bg-white rounded-xl border border-gray-100 shadow-sm overflow-hidden">
        <table className="w-full text-sm">
          <thead className="bg-gray-50/80">
            <tr>
              {['用户itcode', '用户姓名', '账号状态', '分组', '申请理由', '申请状态', '审批备注', '申请时间', '操作'].map((h) => (
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
                <td colSpan={9} className="px-4 py-10 text-center text-sm text-gray-400">暂无数据</td>
              </tr>
            ) : (
              apps.map((app) => (
                <>
                  <tr key={app.id} className="hover:bg-gray-50/50 transition-colors">
                    <td className="px-4 py-3.5 text-gray-500 text-xs">{app.user_itcode}</td>
                    <td className="px-4 py-3.5 text-gray-500 text-xs">{app.user_name || '—'}</td>
                    <td className="px-4 py-3.5">
                      <span className={`inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium ring-1 ${USER_STATUS_CLASS[app.user_status] || ''}`}>
                        {USER_STATUS_LABEL[app.user_status] || app.user_status}
                      </span>
                    </td>
                    <td className="px-4 py-3.5 text-gray-500 text-xs">{app.group_id || '未分组'}</td>
                    <td className="px-4 py-3.5 text-gray-500 max-w-xs truncate">{app.reason}</td>
                    <td className="px-4 py-3.5">
                      <span className={`inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium ring-1 ${APP_STATUS_CLASS[app.status] || ''}`}>
                        {APP_STATUS_LABEL[app.status] || app.status}
                      </span>
                    </td>
                    <td className="px-4 py-3.5 text-gray-400 text-xs">{app.review_note || '—'}</td>
                    <td className="px-4 py-3.5 text-gray-400 text-xs">
                      {formatDate(app.created_at)}
                    </td>
                    <td className="px-4 py-3.5">
                      {app.status === 'pending' && (
                        <button
                          onClick={() => {
                            setReviewId(reviewId === app.id ? null : app.id)
                            setSelectedGroup(app.group_id || null)
                          }}
                          className="text-xs text-red-500 hover:text-red-700 font-medium transition-colors"
                        >
                          审批
                        </button>
                      )}
                    </td>
                  </tr>
                  {reviewId === app.id && (
                    <tr key={`review-${app.id}`}>
                      <td colSpan={9} className="px-4 py-3 bg-blue-50/50 border-l-2 border-blue-300">
                        <div className="flex items-center gap-3">
                          <select
                            value={selectedGroup || ''}
                            onChange={(e) => setSelectedGroup(e.target.value ? Number(e.target.value) : null)}
                            className="px-3.5 py-2 border border-gray-200 rounded-xl text-sm bg-white focus:outline-none focus:ring-2 focus:ring-red-500/30 focus:border-red-400 transition-all"
                          >
                            <option value="">选择分组（可选）</option>
                            {groups.map((g) => (
                              <option key={g.id} value={g.id}>{g.name}</option>
                            ))}
                          </select>
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
                            通过（激活账号）
                          </button>
                          <button
                            onClick={() => handleReview(app.id, 'rejected')}
                            disabled={submitting}
                            className="px-4 py-2 bg-red-500 text-white text-sm font-medium rounded-xl hover:bg-red-600 disabled:opacity-50 transition-colors"
                          >
                            拒绝（停用账号）
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
