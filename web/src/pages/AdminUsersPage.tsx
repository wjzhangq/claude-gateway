import { useEffect, useState } from 'react'
import { adminListUsers, adminUpdateUser, adminCreateUser } from '../api'

interface User {
  id: number
  itcode: string
  role: string
  status: string
  quota_tokens: number
  created_at: string
}

export default function AdminUsersPage() {
  const [users, setUsers] = useState<User[]>([])
  const [loading, setLoading] = useState(true)
  const [showCreate, setShowCreate] = useState(false)
  const [newItcode, setNewItcode] = useState('')
  const [newRole, setNewRole] = useState('user')
  const [newQuota, setNewQuota] = useState('1000000')
  const [creating, setCreating] = useState(false)
  const [error, setError] = useState('')

  const load = () => {
    setLoading(true)
    adminListUsers()
      .then((res) => setUsers(res.data.users || []))
      .finally(() => setLoading(false))
  }

  useEffect(() => { load() }, [])

  const handleToggleStatus = async (u: User) => {
    const newStatus = u.status === 'active' ? 'disabled' : 'active'
    await adminUpdateUser(u.id, { status: newStatus })
    load()
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
        quota_tokens: parseInt(newQuota) || 1000000,
      })
      setShowCreate(false)
      setNewItcode('')
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
      <div className="flex items-center justify-between mb-6">
        <h2 className="text-xl font-semibold text-gray-900">用户管理</h2>
        <button
          onClick={() => setShowCreate(true)}
          className="px-4 py-2 bg-red-600 text-white text-sm rounded-md hover:bg-red-700"
        >
          新建用户
        </button>
      </div>

      {showCreate && (
        <div className="mb-6 bg-white border border-gray-200 rounded-lg p-5">
          <h3 className="text-sm font-medium text-gray-700 mb-4">新建用户</h3>
          <form onSubmit={handleCreate} className="space-y-3">
            <div className="grid grid-cols-3 gap-3">
              <div>
                <label className="block text-sm text-gray-600 mb-1">Itcode</label>
                <input
                  value={newItcode}
                  onChange={(e) => setNewItcode(e.target.value)}
                  className="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-red-500"
                />
              </div>
              <div>
                <label className="block text-sm text-gray-600 mb-1">角色</label>
                <select
                  value={newRole}
                  onChange={(e) => setNewRole(e.target.value)}
                  className="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-red-500"
                >
                  <option value="user">普通用户</option>
                  <option value="admin">管理员</option>
                </select>
              </div>
              <div>
                <label className="block text-sm text-gray-600 mb-1">Token 配额</label>
                <input
                  type="number"
                  value={newQuota}
                  onChange={(e) => setNewQuota(e.target.value)}
                  className="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-red-500"
                />
              </div>
            </div>
            {error && <p className="text-sm text-red-600">{error}</p>}
            <div className="flex gap-2">
              <button
                type="submit"
                disabled={creating}
                className="px-4 py-2 bg-red-600 text-white text-sm rounded-md hover:bg-red-700 disabled:opacity-50"
              >
                {creating ? '创建中...' : '确认'}
              </button>
              <button
                type="button"
                onClick={() => setShowCreate(false)}
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
        ) : (
          <table className="w-full text-sm">
            <thead className="bg-gray-50">
              <tr>
                {['Itcode', '角色', '状态', 'Token 配额', '注册时间', '操作'].map((h) => (
                  <th key={h} className="px-4 py-3 text-left text-xs font-medium text-gray-500">
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100">
              {users.map((u) => (
                <tr key={u.id}>
                  <td className="px-4 py-3 font-medium">{u.itcode}</td>
                  <td className="px-4 py-3">
                    <span
                      className={`px-2 py-0.5 rounded text-xs ${
                        u.role === 'admin'
                          ? 'bg-purple-50 text-purple-700'
                          : 'bg-gray-100 text-gray-600'
                      }`}
                    >
                      {u.role === 'admin' ? '管理员' : '普通用户'}
                    </span>
                  </td>
                  <td className="px-4 py-3">
                    <span
                      className={`px-2 py-0.5 rounded text-xs ${
                        u.status === 'active'
                          ? 'bg-green-50 text-green-700'
                          : 'bg-red-50 text-red-700'
                      }`}
                    >
                      {u.status === 'active' ? '正常' : '禁用'}
                    </span>
                  </td>
                  <td className="px-4 py-3">{u.quota_tokens?.toLocaleString() || '-'}</td>
                  <td className="px-4 py-3 text-gray-400">
                    {new Date(u.created_at).toLocaleDateString()}
                  </td>
                  <td className="px-4 py-3">
                    <button
                      onClick={() => handleToggleStatus(u)}
                      className={`text-xs hover:underline ${
                        u.status === 'active' ? 'text-red-500' : 'text-green-600'
                      }`}
                    >
                      {u.status === 'active' ? '禁用' : '启用'}
                    </button>
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
