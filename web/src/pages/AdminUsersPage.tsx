import { useEffect, useState } from 'react'
import { adminListUsers, adminUpdateUser, adminCreateUser, adminGetUsage, adminGetDailyStats } from '../api'
import {
  BarChart, Bar, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer,
} from 'recharts'

interface User {
  id: number
  itcode: string
  role: string
  status: string
  quota_tokens: number
  created_at: string
  last_used_at: string | null
  requests: number
  cost_usd: number
}

interface UsageLog {
  id: number
  cost_usd: number
  created_at: string
}

interface DailyStat {
  date: string
  cost_usd: number
}

interface EditState {
  role: string
  status: string
  quota_tokens: string
}

function toDateStr(d: Date) {
  return d.toISOString().slice(0, 10)
}

function SkeletonRow() {
  return (
    <tr>
      {[90, 60, 60, 90, 70, 80, 110, 80, 80].map((w, i) => (
        <td key={i} className="px-4 py-3.5">
          <div className="skeleton h-3.5 rounded" style={{ width: w }} />
        </td>
      ))}
    </tr>
  )
}

function UserChartsModal({ user, onClose }: { user: User; onClose: () => void }) {
  const [hourlyData, setHourlyData] = useState<{ hour: string; cost: number }[]>([])
  const [dailyData, setDailyData] = useState<{ date: string; cost: number }[]>([])

  useEffect(() => {
    const today = toDateStr(new Date())
    adminGetUsage({ user_id: user.id, page: 1, page_size: 1000, start_date: today, end_date: today })
      .then((res) => {
        const logs: UsageLog[] = res.data.logs || []
        const buckets: Record<string, number> = {}
        for (let h = 0; h < 24; h++) buckets[String(h).padStart(2, '0')] = 0
        logs.forEach((l) => {
          const h = new Date(l.created_at).getHours()
          buckets[String(h).padStart(2, '0')] += l.cost_usd
        })
        setHourlyData(
          Object.entries(buckets).map(([hour, cost]) => ({ hour: hour + ':00', cost: parseFloat(cost.toFixed(6)) }))
        )
      })

    const end = today
    const start = toDateStr(new Date(new Date(today).getTime() - 13 * 86400000))
    adminGetDailyStats({ user_id: user.id, start_date: start, end_date: end })
      .then((res) => {
        const stats: DailyStat[] = res.data.stats || []
        const map: Record<string, number> = {}
        stats.forEach((s) => { map[s.date] = (map[s.date] || 0) + s.cost_usd })
        const result = []
        for (let i = 13; i >= 0; i--) {
          const d = toDateStr(new Date(new Date(today).getTime() - i * 86400000))
          result.push({ date: d.slice(5), cost: parseFloat((map[d] || 0).toFixed(6)) })
        }
        setDailyData(result)
      })
  }, [user.id])

  return (
    <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50 backdrop-blur-sm" onClick={onClose}>
      <div className="bg-white rounded-2xl shadow-2xl w-[740px] p-6 border border-gray-100" onClick={(e) => e.stopPropagation()}>
        <div className="flex items-center justify-between mb-5">
          <div>
            <h3 className="text-base font-bold text-gray-900">{user.itcode}</h3>
            <p className="text-xs text-gray-400 mt-0.5">使用费用统计</p>
          </div>
          <button onClick={onClose} className="w-7 h-7 flex items-center justify-center rounded-lg text-gray-400 hover:text-gray-600 hover:bg-gray-100 transition-colors text-sm">✕</button>
        </div>
        <div className="grid grid-cols-2 gap-5">
          <div className="bg-gray-50 rounded-xl p-4">
            <p className="text-xs font-semibold text-gray-500 mb-3 uppercase tracking-wide">今日费用（按小时）</p>
            <ResponsiveContainer width="100%" height={180}>
              <BarChart data={hourlyData}>
                <CartesianGrid strokeDasharray="3 3" stroke="#e5e7eb" />
                <XAxis dataKey="hour" tick={{ fontSize: 9 }} interval={3} />
                <YAxis tick={{ fontSize: 10 }} tickFormatter={(v) => `$${v}`} />
                <Tooltip contentStyle={{ borderRadius: 8, border: '1px solid #e5e7eb', fontSize: 12 }} formatter={(v) => [`$${Number(v).toFixed(6)}`, '费用']} />
                <Bar dataKey="cost" fill="#DC2626" radius={[3, 3, 0, 0]} />
              </BarChart>
            </ResponsiveContainer>
          </div>
          <div className="bg-gray-50 rounded-xl p-4">
            <p className="text-xs font-semibold text-gray-500 mb-3 uppercase tracking-wide">近14天费用</p>
            <ResponsiveContainer width="100%" height={180}>
              <BarChart data={dailyData}>
                <CartesianGrid strokeDasharray="3 3" stroke="#e5e7eb" />
                <XAxis dataKey="date" tick={{ fontSize: 10 }} />
                <YAxis tick={{ fontSize: 10 }} tickFormatter={(v) => `$${v}`} />
                <Tooltip contentStyle={{ borderRadius: 8, border: '1px solid #e5e7eb', fontSize: 12 }} formatter={(v) => [`$${Number(v).toFixed(6)}`, '费用']} />
                <Bar dataKey="cost" fill="#DC2626" radius={[3, 3, 0, 0]} />
              </BarChart>
            </ResponsiveContainer>
          </div>
        </div>
      </div>
    </div>
  )
}

export default function AdminUsersPage() {
  const [users, setUsers] = useState<User[]>([])
  const [loading, setLoading] = useState(true)
  const [showCreate, setShowCreate] = useState(false)
  const [newItcode, setNewItcode] = useState('')
  const [newRole, setNewRole] = useState('user')
  const [newQuota, setNewQuota] = useState('0')
  const [creating, setCreating] = useState(false)
  const [error, setError] = useState('')
  const [chartUser, setChartUser] = useState<User | null>(null)
  const [editId, setEditId] = useState<number | null>(null)
  const [editState, setEditState] = useState<EditState>({ role: '', status: '', quota_tokens: '' })
  const [saving, setSaving] = useState(false)

  const load = () => {
    setLoading(true)
    adminListUsers()
      .then((res) => setUsers(res.data.users || []))
      .finally(() => setLoading(false))
  }

  useEffect(() => { load() }, [])

  const openEdit = (u: User) => {
    setEditId(u.id)
    setEditState({ role: u.role, status: u.status, quota_tokens: String(u.quota_tokens ?? 0) })
  }

  const handleSave = async (id: number) => {
    setSaving(true)
    try {
      await adminUpdateUser(id, {
        role: editState.role,
        status: editState.status,
        quota_tokens: parseInt(editState.quota_tokens) || 0,
      })
      setEditId(null)
      load()
    } finally {
      setSaving(false)
    }
  }

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!newItcode) { setError('请输入 itcode'); return }
    setCreating(true)
    setError('')
    try {
      await adminCreateUser({
        itcode: newItcode,
        role: newRole,
        quota_tokens: parseInt(newQuota) || 0,
      })
      setShowCreate(false)
      setNewItcode('')
      setNewQuota('0')
      load()
    } catch (e: unknown) {
      const msg = (e as { response?: { data?: { error?: string } } })?.response?.data?.error
      setError(msg || '创建失败')
    } finally {
      setCreating(false)
    }
  }

  return (
    <div className="p-8">
      {chartUser && <UserChartsModal user={chartUser} onClose={() => setChartUser(null)} />}

      <div className="flex items-center justify-between mb-7">
        <div>
          <h2 className="text-xl font-bold text-gray-900">用户管理</h2>
          <p className="text-sm text-gray-400 mt-0.5">管理系统用户和权限</p>
        </div>
        <button
          onClick={() => setShowCreate(true)}
          className="px-4 py-2 bg-red-600 text-white text-sm font-medium rounded-xl hover:bg-red-700 shadow-sm hover:shadow-md transition-all"
        >
          + 新建用户
        </button>
      </div>

      {showCreate && (
        <div className="mb-6 bg-white border border-gray-100 rounded-xl p-5 shadow-sm">
          <h3 className="text-sm font-semibold text-gray-700 mb-4">新建用户</h3>
          <form onSubmit={handleCreate} className="space-y-3">
            <div className="grid grid-cols-3 gap-3">
              <div>
                <label className="block text-xs font-semibold text-gray-500 mb-1.5 uppercase tracking-wide">Itcode</label>
                <input
                  value={newItcode}
                  onChange={(e) => setNewItcode(e.target.value)}
                  className="w-full px-3.5 py-2.5 border border-gray-200 rounded-xl text-sm bg-gray-50 focus:bg-white focus:outline-none focus:ring-2 focus:ring-red-500/30 focus:border-red-400 transition-all"
                />
              </div>
              <div>
                <label className="block text-xs font-semibold text-gray-500 mb-1.5 uppercase tracking-wide">角色</label>
                <select
                  value={newRole}
                  onChange={(e) => setNewRole(e.target.value)}
                  className="w-full px-3.5 py-2.5 border border-gray-200 rounded-xl text-sm bg-gray-50 focus:bg-white focus:outline-none focus:ring-2 focus:ring-red-500/30 focus:border-red-400 transition-all"
                >
                  <option value="user">普通用户</option>
                  <option value="admin">管理员</option>
                </select>
              </div>
              <div>
                <label className="block text-xs font-semibold text-gray-500 mb-1.5 uppercase tracking-wide">Token 配额</label>
                <input
                  type="number"
                  value={newQuota}
                  onChange={(e) => setNewQuota(e.target.value)}
                  className="w-full px-3.5 py-2.5 border border-gray-200 rounded-xl text-sm bg-gray-50 focus:bg-white focus:outline-none focus:ring-2 focus:ring-red-500/30 focus:border-red-400 transition-all"
                />
              </div>
            </div>
            {error && <p className="text-sm text-red-600">{error}</p>}
            <div className="flex gap-2">
              <button
                type="submit"
                disabled={creating}
                className="px-4 py-2.5 bg-red-600 text-white text-sm font-medium rounded-xl hover:bg-red-700 disabled:opacity-50 transition-colors"
              >
                {creating ? '创建中...' : '确认'}
              </button>
              <button
                type="button"
                onClick={() => setShowCreate(false)}
                className="px-4 py-2.5 text-sm border border-gray-200 rounded-xl hover:bg-gray-50 transition-colors"
              >
                取消
              </button>
            </div>
          </form>
        </div>
      )}

      <div className="bg-white rounded-xl border border-gray-100 shadow-sm overflow-hidden">
        <table className="w-full text-sm">
          <thead className="bg-gray-50/80">
            <tr>
              {['Itcode', '角色', '状态', 'Token 配额', '请求数', '费用', '最后使用', '注册时间', '操作'].map((h) => (
                <th key={h} className="px-4 py-3 text-left text-xs font-semibold text-gray-400 uppercase tracking-wide">
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-50">
            {loading ? (
              Array.from({ length: 4 }).map((_, i) => <SkeletonRow key={i} />)
            ) : (
              users.map((u) => (
                <>
                  <tr key={u.id} className="hover:bg-gray-50/50 transition-colors">
                    <td className="px-4 py-3.5 font-medium text-gray-800">{u.itcode}</td>
                    <td className="px-4 py-3.5">
                      <span
                        className={`inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium ring-1 ${
                          u.role === 'admin'
                            ? 'bg-purple-50 text-purple-700 ring-purple-100'
                            : 'bg-gray-100 text-gray-600 ring-gray-200'
                        }`}
                      >
                        {u.role === 'admin' ? '管理员' : '普通用户'}
                      </span>
                    </td>
                    <td className="px-4 py-3.5">
                      <span
                        className={`inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium ring-1 ${
                          u.status === 'active'
                            ? 'bg-green-50 text-green-700 ring-green-100'
                            : 'bg-red-50 text-red-700 ring-red-100'
                        }`}
                      >
                        {u.status === 'active' ? '正常' : '禁用'}
                      </span>
                    </td>
                    <td className="px-4 py-3.5 text-gray-600">{u.quota_tokens?.toLocaleString() || '0'}</td>
                    <td className="px-4 py-3.5 text-gray-600">{(u.requests || 0).toLocaleString()}</td>
                    <td className="px-4 py-3.5 font-medium text-gray-800">${(u.cost_usd || 0).toFixed(4)}</td>
                    <td className="px-4 py-3.5 text-gray-400 text-xs">
                      {u.last_used_at ? new Date(u.last_used_at).toLocaleString() : '—'}
                    </td>
                    <td className="px-4 py-3.5 text-gray-400 text-xs">
                      {new Date(u.created_at).toLocaleDateString()}
                    </td>
                    <td className="px-4 py-3.5">
                      <div className="flex items-center gap-3">
                        <button
                          onClick={() => setChartUser(u)}
                          className="text-xs text-blue-500 hover:text-blue-700 transition-colors"
                        >
                          图表
                        </button>
                        <button
                          onClick={() => editId === u.id ? setEditId(null) : openEdit(u)}
                          className="text-xs text-red-500 hover:text-red-700 font-medium transition-colors"
                        >
                          编辑
                        </button>
                      </div>
                    </td>
                  </tr>
                  {editId === u.id && (
                    <tr key={`edit-${u.id}`}>
                      <td colSpan={9} className="px-4 py-3 bg-amber-50/40 border-l-2 border-amber-400">
                        <div className="flex items-center gap-3 flex-wrap">
                          <div className="flex items-center gap-1.5">
                            <label className="text-xs text-gray-500 font-medium">角色</label>
                            <select
                              value={editState.role}
                              onChange={(e) => setEditState((s) => ({ ...s, role: e.target.value }))}
                              className="px-2.5 py-1.5 border border-gray-200 rounded-lg text-sm bg-white focus:outline-none focus:ring-2 focus:ring-red-500/30 focus:border-red-400 transition-all"
                            >
                              <option value="user">普通用户</option>
                              <option value="admin">管理员</option>
                            </select>
                          </div>
                          <div className="flex items-center gap-1.5">
                            <label className="text-xs text-gray-500 font-medium">状态</label>
                            <select
                              value={editState.status}
                              onChange={(e) => setEditState((s) => ({ ...s, status: e.target.value }))}
                              className="px-2.5 py-1.5 border border-gray-200 rounded-lg text-sm bg-white focus:outline-none focus:ring-2 focus:ring-red-500/30 focus:border-red-400 transition-all"
                            >
                              <option value="active">正常</option>
                              <option value="disabled">禁用</option>
                            </select>
                          </div>
                          <div className="flex items-center gap-1.5">
                            <label className="text-xs text-gray-500 font-medium">Token 配额</label>
                            <input
                              type="number"
                              value={editState.quota_tokens}
                              onChange={(e) => setEditState((s) => ({ ...s, quota_tokens: e.target.value }))}
                              className="w-32 px-2.5 py-1.5 border border-gray-200 rounded-lg text-sm bg-white focus:outline-none focus:ring-2 focus:ring-red-500/30 focus:border-red-400 transition-all"
                            />
                          </div>
                          <button
                            onClick={() => handleSave(u.id)}
                            disabled={saving}
                            className="px-4 py-1.5 bg-red-600 text-white text-sm font-medium rounded-lg hover:bg-red-700 disabled:opacity-50 transition-colors"
                          >
                            {saving ? '保存中...' : '保存'}
                          </button>
                          <button
                            onClick={() => setEditId(null)}
                            className="px-4 py-1.5 text-sm border border-gray-200 rounded-lg hover:bg-gray-100 transition-colors"
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
