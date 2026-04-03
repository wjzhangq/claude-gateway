import { useEffect, useState } from 'react'
import { adminListAWSUsers, adminToggleAWSUser, adminEnableAWSByItcode } from '../api'

interface AWSUser {
  id: number
  itcode: string
  name: string
  status: string
  aws_enabled: boolean
  aws_requests: number
  aws_cost_usd: number
}

function SkeletonRow() {
  return (
    <tr>
      {[40, 100, 80, 60, 80, 80, 100, 60].map((w, i) => (
        <td key={i} className="px-4 py-3.5">
          <div className="skeleton h-3.5 rounded" style={{ width: w }} />
        </td>
      ))}
    </tr>
  )
}

export default function AdminAWSUsersPage() {
  const [users, setUsers] = useState<AWSUser[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [loading, setLoading] = useState(true)
  const pageSize = 20

  const [addItcode, setAddItcode] = useState('')
  const [adding, setAdding] = useState(false)
  const [addError, setAddError] = useState('')
  const [addSuccess, setAddSuccess] = useState('')

  const load = (p: number) => {
    setLoading(true)
    adminListAWSUsers({ page: p, page_size: pageSize })
      .then((res) => {
        setUsers(res.data.users || [])
        setTotal(res.data.total || 0)
      })
      .catch(() => {})
      .finally(() => setLoading(false))
  }

  useEffect(() => { load(page) }, [page])

  const handleToggle = async (u: AWSUser) => {
    await adminToggleAWSUser(u.id)
    load(page)
  }

  const handleAdd = async () => {
    const itcode = addItcode.trim()
    if (!itcode) { setAddError('请输入 itcode'); return }
    setAdding(true)
    setAddError('')
    setAddSuccess('')
    try {
      const res = await adminEnableAWSByItcode(itcode)
      const msg = res.data.message
      setAddSuccess(msg ? msg : `已为 ${res.data.itcode ?? itcode} 开启 AWS`)
      setAddItcode('')
      load(1)
      setPage(1)
    } catch (e: unknown) {
      const msg = (e as { response?: { data?: { error?: string } } })?.response?.data?.error
      setAddError(msg || '操作失败')
    } finally {
      setAdding(false)
    }
  }

  const totalPages = Math.ceil(total / pageSize)

  return (
    <div className="p-8">
      <div className="flex items-center justify-between mb-7">
        <div>
          <h2 className="text-xl font-bold text-gray-900">AWS 用户管理</h2>
          <p className="text-sm text-gray-400 mt-0.5">管理用户的 AWS Bedrock 渠道访问权限</p>
        </div>
      </div>

      {/* Add user by itcode */}
      <div className="bg-white rounded-xl border border-gray-100 p-5 mb-5 shadow-sm">
        <h3 className="text-sm font-semibold text-gray-700 mb-3">添加 AWS 用户</h3>
        <div className="flex items-center gap-2">
          <input
            value={addItcode}
            onChange={(e) => { setAddItcode(e.target.value); setAddError(''); setAddSuccess('') }}
            onKeyDown={(e) => { if (e.key === 'Enter') handleAdd() }}
            placeholder="输入用户 itcode"
            className="w-64 px-3.5 py-2 border border-gray-200 rounded-xl text-sm bg-gray-50 focus:bg-white focus:outline-none focus:ring-2 focus:ring-amber-500/30 focus:border-amber-400 transition-all"
          />
          <button
            onClick={handleAdd}
            disabled={adding}
            className="px-4 py-2 bg-amber-600 text-white text-sm font-medium rounded-xl hover:bg-amber-700 disabled:opacity-50 transition-colors"
          >
            {adding ? '处理中...' : '开启 AWS'}
          </button>
        </div>
        {addError && <p className="mt-2 text-sm text-red-600">{addError}</p>}
        {addSuccess && <p className="mt-2 text-sm text-green-600">{addSuccess}</p>}
      </div>

      <div className="bg-white rounded-xl border border-gray-100 shadow-sm overflow-hidden">
        <div className="px-6 py-4 border-b border-gray-100 flex items-center justify-between">
          <h3 className="text-sm font-semibold text-gray-700">AWS 用户列表</h3>
          <span className="text-xs text-gray-400">共 {total} 位用户</span>
        </div>
        <table className="w-full text-sm">
          <thead className="bg-gray-50/80">
            <tr>
              {['ID', '用户', '姓名', '状态', 'AWS 权限', '总请求', '总费用', '操作'].map((h) => (
                <th key={h} className="px-4 py-3 text-left text-xs font-semibold text-gray-400 uppercase tracking-wide">
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-50">
            {loading ? (
              Array.from({ length: 5 }).map((_, i) => <SkeletonRow key={i} />)
            ) : users.length === 0 ? (
              <tr>
                <td colSpan={8} className="px-4 py-10 text-center text-sm text-gray-400">暂无数据</td>
              </tr>
            ) : (
              users.map((u) => (
                <tr key={u.id} className="hover:bg-gray-50/50 transition-colors">
                  <td className="px-4 py-3.5 text-gray-400 text-xs">{u.id}</td>
                  <td className="px-4 py-3.5 font-medium text-gray-800">{u.itcode}</td>
                  <td className="px-4 py-3.5 text-gray-600">{u.name}</td>
                  <td className="px-4 py-3.5">
                    <span className={`inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium ring-1 ${
                      u.status === 'active'
                        ? 'bg-green-50 text-green-700 ring-green-100'
                        : 'bg-gray-100 text-gray-500 ring-gray-200'
                    }`}>
                      {u.status === 'active' ? '正常' : u.status}
                    </span>
                  </td>
                  <td className="px-4 py-3.5">
                    <span className={`inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium ring-1 ${
                      u.aws_enabled
                        ? 'bg-amber-50 text-amber-700 ring-amber-100'
                        : 'bg-gray-100 text-gray-500 ring-gray-200'
                    }`}>
                      {u.aws_enabled ? '已开启' : '未开启'}
                    </span>
                  </td>
                  <td className="px-4 py-3.5 text-gray-600 text-xs">{(u.aws_requests || 0).toLocaleString()}</td>
                  <td className="px-4 py-3.5 font-medium text-amber-700">${(u.aws_cost_usd || 0).toFixed(4)}</td>
                  <td className="px-4 py-3.5">
                    <button
                      onClick={() => handleToggle(u)}
                      className={`text-xs font-medium transition-colors ${
                        u.aws_enabled
                          ? 'text-red-500 hover:text-red-700'
                          : 'text-amber-600 hover:text-amber-800'
                      }`}
                    >
                      {u.aws_enabled ? '关闭 AWS' : '开启 AWS'}
                    </button>
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </table>
        {totalPages > 1 && (
          <div className="px-6 py-4 border-t border-gray-100 flex items-center gap-3">
            <button
              onClick={() => setPage((p) => Math.max(1, p - 1))}
              disabled={page === 1}
              className="px-3.5 py-1.5 text-sm border border-gray-200 rounded-lg hover:bg-gray-50 disabled:opacity-40 transition-colors"
            >
              上一页
            </button>
            <span className="text-sm text-gray-500">{page} / {totalPages}</span>
            <button
              onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
              disabled={page === totalPages}
              className="px-3.5 py-1.5 text-sm border border-gray-200 rounded-lg hover:bg-gray-50 disabled:opacity-40 transition-colors"
            >
              下一页
            </button>
          </div>
        )}
      </div>
    </div>
  )
}
