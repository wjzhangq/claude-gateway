import { useEffect, useState } from 'react'
import { adminListUsers, adminUpdateUser, adminCreateUser, adminGetUsage, adminGetDailyStats, adminGetGroups, adminUpdateItcode, adminTransferKey, adminListKeys } from '../api'
import { formatTime, formatDate, toDateStr } from '../utils/time'
import {
  BarChart, Bar, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer,
} from 'recharts'

interface User {
  id: number
  itcode: string
  name: string
  role: string
  status: string
  group_id: number
  daily_quota_usd: number
  aws_daily_quota_usd: number
  created_at: string
  last_used_at: string | null
  requests: number
  cost_usd: number
  oc_cost_usd: number
  backend_cost_usd: number
  aws_cost_usd: number
}

interface Group {
  id: number
  name: string
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
  group_id: number
  daily_quota_usd: string
  aws_daily_quota_usd: string
  name: string
}

function SkeletonRow() {
  return (
    <tr>
      {[90, 60, 60, 70, 70, 80, 70, 80, 80, 80, 110, 80, 80].map((w, i) => (
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
  const [groups, setGroups] = useState<Group[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const pageSize = 20
  const [showCreate, setShowCreate] = useState(false)
  const [newItcode, setNewItcode] = useState('')
  const [newRole, setNewRole] = useState('user')
  const [newGroup, setNewGroup] = useState(0)
  const [newStatus, setNewStatus] = useState('pending')
  const [newQuota, setNewQuota] = useState('0')
  const [creating, setCreating] = useState(false)
  const [error, setError] = useState('')
  const [chartUser, setChartUser] = useState<User | null>(null)
  const [editId, setEditId] = useState<number | null>(null)
  const [editState, setEditState] = useState<EditState>({ role: '', status: '', group_id: 0, daily_quota_usd: '', aws_daily_quota_usd: '', name: '' })
  const [saving, setSaving] = useState(false)
  const [sortKey, setSortKey] = useState<string>('id')
  const [sortOrder, setSortOrder] = useState<'asc' | 'desc'>('desc')
  const [searchQuery, setSearchQuery] = useState('')
  // itcode rename
  const [renamingItcodeId, setRenamingItcodeId] = useState<number | null>(null)
  const [itcodeValue, setItcodeValue] = useState('')
  // key transfer modal
  const [transferUserId, setTransferUserId] = useState<number | null>(null)
  const [transferUserItcode, setTransferUserItcode] = useState('')
  const [userKeys, setUserKeys] = useState<{ id: number; name: string; key: string }[]>([])
  const [selectedKeyId, setSelectedKeyId] = useState<number | null>(null)
  const [transferTarget, setTransferTarget] = useState('')
  const [transferring, setTransferring] = useState(false)

  const load = (p = page) => {
    setLoading(true)
    adminListUsers({ page: p, page_size: pageSize })
      .then((res) => { setUsers(res.data.users || []); setTotal(res.data.total || 0) })
      .catch(() => {})
      .finally(() => setLoading(false))
  }

  const loadGroups = () => {
    adminGetGroups().then((res) => setGroups(res.data.groups || [])).catch(() => {})
  }

  useEffect(() => { load(); loadGroups() }, [])
  useEffect(() => { load(page) }, [page])

  const handleSort = (key: string) => {
    if (sortKey === key) {
      setSortOrder(sortOrder === 'asc' ? 'desc' : 'asc')
    } else {
      setSortKey(key)
      setSortOrder('desc')
    }
  }

  const filteredUsers = searchQuery.trim()
    ? users.filter((u) => {
        const q = searchQuery.trim().toLowerCase()
        return (
          u.itcode.toLowerCase().includes(q) ||
          (u.name ?? '').toLowerCase().includes(q)
        )
      })
    : users

  const sortedUsers = [...filteredUsers].sort((a, b) => {
    let aVal: string | number = a.id
    let bVal: string | number = b.id
    if (sortKey === 'itcode') { aVal = a.itcode; bVal = b.itcode }
    else if (sortKey === 'role') { aVal = a.role; bVal = b.role }
    else if (sortKey === 'status') { aVal = a.status; bVal = b.status }
    else if (sortKey === 'group_id') { aVal = a.group_id; bVal = b.group_id }
    else if (sortKey === 'daily_quota_usd') { aVal = a.daily_quota_usd || 0; bVal = b.daily_quota_usd || 0 }
    else if (sortKey === 'aws_daily_quota_usd') { aVal = a.aws_daily_quota_usd || 0; bVal = b.aws_daily_quota_usd || 0 }
    else if (sortKey === 'requests') { aVal = a.requests || 0; bVal = b.requests || 0 }
    else if (sortKey === 'cost_usd') { aVal = a.cost_usd || 0; bVal = b.cost_usd || 0 }
    else if (sortKey === 'backend_cost_usd') { aVal = a.backend_cost_usd || 0; bVal = b.backend_cost_usd || 0 }
    else if (sortKey === 'aws_cost_usd') { aVal = a.aws_cost_usd || 0; bVal = b.aws_cost_usd || 0 }
    else if (sortKey === 'created_at') { aVal = a.created_at; bVal = b.created_at }
    else if (sortKey === 'last_used_at') { aVal = a.last_used_at || ''; bVal = b.last_used_at || '' }

    if (typeof aVal === 'string' && typeof bVal === 'string') {
      return sortOrder === 'asc' ? aVal.localeCompare(bVal) : bVal.localeCompare(aVal)
    }
    return sortOrder === 'asc' ? (aVal as number) - (bVal as number) : (bVal as number) - (aVal as number)
  })

  const sortIcon = (key: string) => {
    if (sortKey !== key) return <span className="ml-1 opacity-30">↕</span>
    return <span className="ml-1">{sortOrder === 'asc' ? '↑' : '↓'}</span>
  }

  const openEdit = (u: User) => {
    setEditId(u.id)
    setEditState({
      role: u.role,
      status: u.status,
      group_id: u.group_id ?? 0,
      daily_quota_usd: String(u.daily_quota_usd ?? 0),
      aws_daily_quota_usd: String(u.aws_daily_quota_usd ?? 0),
      name: u.name ?? '',
    })
  }

  const handleSave = async (id: number) => {
    setSaving(true)
    try {
      await adminUpdateUser(id, {
        name: editState.name,
        role: editState.role,
        status: editState.status,
        group_id: editState.group_id,
        daily_quota_usd: parseFloat(editState.daily_quota_usd) || 0,
        aws_daily_quota_usd: parseFloat(editState.aws_daily_quota_usd) || 0,
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
        status: newStatus,
        group_id: newGroup,
        daily_quota_usd: parseFloat(newQuota) || 0,
      })
      setShowCreate(false)
      setNewItcode('')
      setNewGroup(0)
      setNewQuota('0')
      load()
    } catch (e: unknown) {
      const msg = (e as { response?: { data?: { error?: string } } })?.response?.data?.error
      setError(msg || '创建失败')
    } finally {
      setCreating(false)
    }
  }

  const handleItcodeRename = async (id: number) => {
    if (!itcodeValue.trim()) return
    await adminUpdateItcode(id, itcodeValue.trim())
    setRenamingItcodeId(null)
    load()
  }

  const openTransfer = async (u: User) => {
    setTransferUserId(u.id)
    setTransferUserItcode(u.itcode)
    setTransferTarget('')
    setSelectedKeyId(null)
    const res = await adminListKeys({ user_id: u.id, page: 1, page_size: 100 })
    setUserKeys(res.data.keys || [])
  }

  const handleTransfer = async () => {
    if (!selectedKeyId || !transferTarget.trim()) return
    setTransferring(true)
    try {
      await adminTransferKey(selectedKeyId, transferTarget.trim())
      setTransferUserId(null)
      load()
    } finally {
      setTransferring(false)
    }
  }

  const totalPages = Math.ceil(total / pageSize)

  return (
    <div className="p-8">
      {chartUser && <UserChartsModal user={chartUser} onClose={() => setChartUser(null)} />}

      {/* Key Transfer Modal */}
      {transferUserId !== null && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50 backdrop-blur-sm" onClick={() => setTransferUserId(null)}>
          <div className="bg-white rounded-2xl shadow-2xl w-[480px] p-6 border border-gray-100" onClick={(e) => e.stopPropagation()}>
            <div className="flex items-center justify-between mb-5">
              <div>
                <h3 className="text-base font-bold text-gray-900">Key 转移</h3>
                <p className="text-xs text-gray-400 mt-0.5">将 {transferUserItcode} 的 Key 转移给其他用户</p>
              </div>
              <button onClick={() => setTransferUserId(null)} className="w-7 h-7 flex items-center justify-center rounded-lg text-gray-400 hover:text-gray-600 hover:bg-gray-100 transition-colors text-sm">✕</button>
            </div>
            <div className="space-y-4">
              <div>
                <label className="block text-xs font-semibold text-gray-500 mb-1.5 uppercase tracking-wide">选择要转移的 Key</label>
                {userKeys.length === 0 ? (
                  <p className="text-sm text-gray-400">该用户暂无 Key</p>
                ) : (
                  <div className="space-y-1.5 max-h-40 overflow-y-auto">
                    {userKeys.map((k) => (
                      <label key={k.id} className={`flex items-center gap-2.5 px-3 py-2 rounded-lg border cursor-pointer transition-colors ${selectedKeyId === k.id ? 'border-red-400 bg-red-50' : 'border-gray-200 hover:bg-gray-50'}`}>
                        <input type="radio" name="transfer_key" value={k.id} checked={selectedKeyId === k.id} onChange={() => setSelectedKeyId(k.id)} className="accent-red-600" />
                        <span className="text-sm font-medium text-gray-700">{k.name || '(无名称)'}</span>
                        <span className="text-xs text-gray-400 font-mono">{k.key.slice(0, 12)}...</span>
                      </label>
                    ))}
                  </div>
                )}
              </div>
              <div>
                <label className="block text-xs font-semibold text-gray-500 mb-1.5 uppercase tracking-wide">目标用户 Itcode</label>
                <input
                  value={transferTarget}
                  onChange={(e) => setTransferTarget(e.target.value)}
                  placeholder="输入目标用户 itcode（不存在则自动创建并激活）"
                  className="w-full px-3.5 py-2.5 border border-gray-200 rounded-xl text-sm bg-gray-50 focus:bg-white focus:outline-none focus:ring-2 focus:ring-red-500/30 focus:border-red-400 transition-all"
                />
              </div>
              <div className="flex gap-2 pt-1">
                <button
                  onClick={handleTransfer}
                  disabled={transferring || !selectedKeyId || !transferTarget.trim()}
                  className="flex-1 px-4 py-2.5 bg-red-600 text-white text-sm font-medium rounded-xl hover:bg-red-700 disabled:opacity-50 transition-colors"
                >
                  {transferring ? '转移中...' : '确认转移'}
                </button>
                <button onClick={() => setTransferUserId(null)} className="px-4 py-2.5 text-sm border border-gray-200 rounded-xl hover:bg-gray-50 transition-colors">取消</button>
              </div>
            </div>
          </div>
        </div>
      )}

      <div className="flex items-center justify-between mb-7">
        <div>
          <h2 className="text-xl font-bold text-gray-900">用户管理</h2>
          <p className="text-sm text-gray-400 mt-0.5">管理系统用户和权限</p>
        </div>
        <div className="flex items-center gap-3">
          <input
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
            placeholder="搜索 itcode / 名称..."
            className="w-56 px-3.5 py-2 border border-gray-200 rounded-xl text-sm bg-gray-50 focus:bg-white focus:outline-none focus:ring-2 focus:ring-red-500/30 focus:border-red-400 transition-all"
          />
          <button
            onClick={() => setShowCreate(true)}
            className="px-4 py-2 bg-red-600 text-white text-sm font-medium rounded-xl hover:bg-red-700 shadow-sm hover:shadow-md transition-all"
          >
            + 新建用户
          </button>
        </div>
      </div>

      {showCreate && (
        <div className="mb-6 bg-white border border-gray-100 rounded-xl p-5 shadow-sm">
          <h3 className="text-sm font-semibold text-gray-700 mb-4">新建用户</h3>
          <form onSubmit={handleCreate} className="space-y-3">
            <div className="grid grid-cols-5 gap-3">
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
                <label className="block text-xs font-semibold text-gray-500 mb-1.5 uppercase tracking-wide">状态</label>
                <select
                  value={newStatus}
                  onChange={(e) => setNewStatus(e.target.value)}
                  className="w-full px-3.5 py-2.5 border border-gray-200 rounded-xl text-sm bg-gray-50 focus:bg-white focus:outline-none focus:ring-2 focus:ring-red-500/30 focus:border-red-400 transition-all"
                >
                  <option value="pending">待审核</option>
                  <option value="active">正常</option>
                  <option value="disabled">禁用</option>
                </select>
              </div>
              <div>
                <label className="block text-xs font-semibold text-gray-500 mb-1.5 uppercase tracking-wide">分组</label>
                <select
                  value={newGroup}
                  onChange={(e) => setNewGroup(Number(e.target.value))}
                  className="w-full px-3.5 py-2.5 border border-gray-200 rounded-xl text-sm bg-gray-50 focus:bg-white focus:outline-none focus:ring-2 focus:ring-red-500/30 focus:border-red-400 transition-all"
                >
                  <option value={0}>未分组</option>
                  {groups.map((g) => (
                    <option key={g.id} value={g.id}>{g.name}</option>
                  ))}
                </select>
              </div>
              <div>
                <label className="block text-xs font-semibold text-gray-500 mb-1.5 uppercase tracking-wide">Token 每日最大费用</label>
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
              {[
                { key: 'itcode', label: 'Itcode' },
                { key: 'role', label: '角色' },
                { key: 'status', label: '状态' },
                { key: 'group_id', label: '分组' },
                { key: 'daily_quota_usd', label: '日限额$' },
                { key: 'aws_daily_quota_usd', label: 'AWS日限额$' },
                { key: 'requests', label: '请求数' },
                { key: 'backend_cost_usd', label: 'Backend 费用' },
                { key: 'oc_cost_usd', label: 'OC 费用' },
                { key: 'aws_cost_usd', label: 'AWS 费用' },
                { key: 'last_used_at', label: '最后使用' },
                { key: 'created_at', label: '注册时间' },
                { key: '', label: '操作' },
              ].map((col) => (
                <th
                  key={col.key}
                  className={`px-4 py-3 text-left text-xs font-semibold uppercase tracking-wide ${col.key ? 'cursor-pointer hover:text-gray-600 transition-colors' : ''}`}
                  onClick={() => col.key && handleSort(col.key)}
                >
                  {col.label}{col.key && sortIcon(col.key)}
                </th>
              ))}
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-50">
            {loading ? (
              Array.from({ length: 4 }).map((_, i) => <SkeletonRow key={i} />)
            ) : (
              sortedUsers.map((u) => (
                <>
                  <tr key={u.id} className="hover:bg-gray-50/50 transition-colors">
                    <td className="px-4 py-3.5 font-medium text-gray-800">
                      {renamingItcodeId === u.id ? (
                        <div className="flex items-center gap-1.5">
                          <input
                            value={itcodeValue}
                            onChange={(e) => setItcodeValue(e.target.value)}
                            onKeyDown={(e) => { if (e.key === 'Enter') handleItcodeRename(u.id); if (e.key === 'Escape') setRenamingItcodeId(null) }}
                            className="w-28 px-2 py-1 border border-red-300 rounded-lg text-xs focus:outline-none focus:ring-1 focus:ring-red-400"
                            autoFocus
                          />
                          <button onClick={() => handleItcodeRename(u.id)} className="text-xs text-green-600 hover:text-green-800">✓</button>
                          <button onClick={() => setRenamingItcodeId(null)} className="text-xs text-gray-400 hover:text-gray-600">✕</button>
                        </div>
                      ) : (
                        <span className="cursor-pointer hover:text-red-600 transition-colors" title="双击改名" onDoubleClick={() => { setRenamingItcodeId(u.id); setItcodeValue(u.itcode) }}>{u.itcode}</span>
                      )}
                    </td>
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
                            : u.status === 'pending'
                            ? 'bg-yellow-50 text-yellow-700 ring-yellow-100'
                            : 'bg-red-50 text-red-700 ring-red-100'
                        }`}
                      >
                        {u.status === 'active' ? '正常' : u.status === 'pending' ? '待审核' : '禁用'}
                      </span>
                    </td>
                    <td className="px-4 py-3.5 text-gray-600">
                      {groups.find((g) => g.id === u.group_id)?.name || (u.group_id ? `分组${u.group_id}` : '未分组')}
                    </td>
                    <td className="px-4 py-3.5 text-gray-600">{u.daily_quota_usd != null ? u.daily_quota_usd.toFixed(2) : '0.00'}</td>
                    <td className="px-4 py-3.5 text-gray-600">{u.aws_daily_quota_usd != null ? u.aws_daily_quota_usd.toFixed(2) : '0.00'}</td>
                    <td className="px-4 py-3.5 text-gray-600">{(u.requests || 0).toLocaleString()}</td>
                    <td className="px-4 py-3.5 font-medium text-gray-800">${(u.backend_cost_usd || 0).toFixed(4)}</td>
                    <td className="px-4 py-3.5 text-orange-600 font-medium">
                      ${(u.oc_cost_usd || 0).toFixed(4)}
                      {u.backend_cost_usd > 0 && (
                        <span className="ml-1 text-[10px] text-gray-400">
                          ({((u.oc_cost_usd || 0) / u.backend_cost_usd * 100).toFixed(0)}%)
                        </span>
                      )}
                    </td>
                    <td className="px-4 py-3.5 font-medium text-amber-700">${(u.aws_cost_usd || 0).toFixed(4)}</td>
                    <td className="px-4 py-3.5 text-gray-400 text-xs">
                      {formatTime(u.last_used_at)}
                    </td>
                    <td className="px-4 py-3.5 text-gray-400 text-xs">
                      {formatDate(u.created_at)}
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
                          onClick={() => openTransfer(u)}
                          className="text-xs text-purple-500 hover:text-purple-700 transition-colors"
                        >
                          转移Key
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
                      <td colSpan={13} className="px-4 py-3 bg-amber-50/40 border-l-2 border-amber-400">
                        <div className="flex items-center gap-3 flex-wrap">
                          <div className="flex items-center gap-1.5">
                            <label className="text-xs text-gray-500 font-medium">名称</label>
                            <input
                              value={editState.name}
                              onChange={(e) => setEditState((s) => ({ ...s, name: e.target.value }))}
                              className="w-28 px-2.5 py-1.5 border border-gray-200 rounded-lg text-sm bg-white focus:outline-none focus:ring-2 focus:ring-red-500/30 focus:border-red-400 transition-all"
                            />
                          </div>
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
                              <option value="pending">待审核</option>
                              <option value="active">正常</option>
                              <option value="disabled">禁用</option>
                            </select>
                          </div>
                          <div className="flex items-center gap-1.5">
                            <label className="text-xs text-gray-500 font-medium">分组</label>
                            <select
                              value={editState.group_id}
                              onChange={(e) => setEditState((s) => ({ ...s, group_id: Number(e.target.value) }))}
                              className="px-2.5 py-1.5 border border-gray-200 rounded-lg text-sm bg-white focus:outline-none focus:ring-2 focus:ring-red-500/30 focus:border-red-400 transition-all"
                            >
                              <option value={0}>未分组</option>
                              {groups.map((g) => (
                                <option key={g.id} value={g.id}>{g.name}</option>
                              ))}
                            </select>
                          </div>
                          <div className="flex items-center gap-1.5">
                            <label className="text-xs text-gray-500 font-medium">日限额$</label>
                            <input
                              type="number"
                              step="0.01"
                              value={editState.daily_quota_usd}
                              onChange={(e) => setEditState((s) => ({ ...s, daily_quota_usd: e.target.value }))}
                              className="w-28 px-2.5 py-1.5 border border-gray-200 rounded-lg text-sm bg-white focus:outline-none focus:ring-2 focus:ring-red-500/30 focus:border-red-400 transition-all"
                            />
                          </div>
                          <div className="flex items-center gap-1.5">
                            <label className="text-xs text-gray-500 font-medium">AWS日限额$</label>
                            <input
                              type="number"
                              step="0.01"
                              value={editState.aws_daily_quota_usd}
                              onChange={(e) => setEditState((s) => ({ ...s, aws_daily_quota_usd: e.target.value }))}
                              className="w-28 px-2.5 py-1.5 border border-gray-200 rounded-lg text-sm bg-white focus:outline-none focus:ring-2 focus:ring-red-500/30 focus:border-red-400 transition-all"
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

      {/* Pagination */}
      {totalPages > 1 && (
        <div className="flex items-center justify-between mt-4">
          <p className="text-sm text-gray-400">共 {total} 条，第 {page}/{totalPages} 页</p>
          <div className="flex items-center gap-1.5">
            <button onClick={() => setPage(1)} disabled={page === 1} className="px-2.5 py-1.5 text-xs border border-gray-200 rounded-lg hover:bg-gray-50 disabled:opacity-40 transition-colors">«</button>
            <button onClick={() => setPage(p => Math.max(1, p - 1))} disabled={page === 1} className="px-2.5 py-1.5 text-xs border border-gray-200 rounded-lg hover:bg-gray-50 disabled:opacity-40 transition-colors">‹</button>
            {Array.from({ length: Math.min(5, totalPages) }, (_, i) => {
              const start = Math.max(1, Math.min(page - 2, totalPages - 4))
              return start + i
            }).map(p => (
              <button key={p} onClick={() => setPage(p)} className={`px-2.5 py-1.5 text-xs border rounded-lg transition-colors ${p === page ? 'bg-red-600 text-white border-red-600' : 'border-gray-200 hover:bg-gray-50'}`}>{p}</button>
            ))}
            <button onClick={() => setPage(p => Math.min(totalPages, p + 1))} disabled={page === totalPages} className="px-2.5 py-1.5 text-xs border border-gray-200 rounded-lg hover:bg-gray-50 disabled:opacity-40 transition-colors">›</button>
            <button onClick={() => setPage(totalPages)} disabled={page === totalPages} className="px-2.5 py-1.5 text-xs border border-gray-200 rounded-lg hover:bg-gray-50 disabled:opacity-40 transition-colors">»</button>
          </div>
        </div>
      )}
    </div>
  )
}
