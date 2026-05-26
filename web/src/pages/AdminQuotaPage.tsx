import { useEffect, useState } from 'react'
import { adminGetConfigLimits, adminUpdateConfigLimits } from '../api'
import { toast } from '../components/Toast'

interface UserLimit {
  itcode: string
  backend_daily_usd: number
  aws_daily_usd: number
}

interface LimitsData {
  backend_daily_max: number
  aws_daily_max: number
  user_daily_limits: UserLimit[]
}

export default function AdminQuotaPage() {
  const [data, setData] = useState<LimitsData | null>(null)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [backendMax, setBackendMax] = useState('')
  const [awsMax, setAwsMax] = useState('')
  const [editIdx, setEditIdx] = useState<number | null>(null)
  const [editBackend, setEditBackend] = useState('')
  const [editAws, setEditAws] = useState('')

  const fetchData = () => {
    setLoading(true)
    adminGetConfigLimits()
      .then((res) => {
        setData(res.data)
        setBackendMax(String(res.data.backend_daily_max))
        setAwsMax(String(res.data.aws_daily_max))
      })
      .catch(() => setData(null))
      .finally(() => setLoading(false))
  }

  useEffect(() => { fetchData() }, [])

  const saveGlobal = async () => {
    setSaving(true)
    try {
      await adminUpdateConfigLimits({
        backend_daily_max: Number(backendMax),
        aws_daily_max: Number(awsMax),
      })
      toast('全局额度已更新')
      fetchData()
    } catch { /* handled by interceptor */ }
    setSaving(false)
  }

  const saveUser = async (idx: number) => {
    if (!data) return
    const user = data.user_daily_limits[idx]
    setSaving(true)
    try {
      await adminUpdateConfigLimits({
        user_limits: [{
          itcode: user.itcode,
          backend_daily_usd: Number(editBackend),
          aws_daily_usd: Number(editAws),
        }],
      })
      toast(`${user.itcode} 额度已更新`)
      setEditIdx(null)
      fetchData()
    } catch { /* handled by interceptor */ }
    setSaving(false)
  }

  const startEdit = (idx: number) => {
    if (!data) return
    const u = data.user_daily_limits[idx]
    setEditIdx(idx)
    setEditBackend(String(u.backend_daily_usd))
    setEditAws(String(u.aws_daily_usd))
  }

  return (
    <div className="p-8">
      <div className="mb-7">
        <h2 className="text-xl font-bold text-gray-900">额度设置</h2>
        <p className="text-sm text-gray-400 mt-0.5">管理全局每日额度上限和用户额度覆盖</p>
      </div>

      {/* 全局设置 */}
      <div className="bg-white rounded-xl border border-gray-100 shadow-sm p-6 mb-6">
        <h3 className="text-sm font-semibold text-gray-700 uppercase tracking-wide mb-4">全局每日上限</h3>
        <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
          <div>
            <label className="block text-sm text-gray-500 mb-1">Backend 每日上限 (USD)</label>
            <input
              type="number"
              value={backendMax}
              onChange={(e) => setBackendMax(e.target.value)}
              className="w-full px-3 py-2 border border-gray-200 rounded-lg focus:ring-2 focus:ring-red-500/20 focus:border-red-400 outline-none transition"
              disabled={loading}
            />
          </div>
          <div>
            <label className="block text-sm text-gray-500 mb-1">AWS 每日上限 (USD)</label>
            <input
              type="number"
              value={awsMax}
              onChange={(e) => setAwsMax(e.target.value)}
              className="w-full px-3 py-2 border border-gray-200 rounded-lg focus:ring-2 focus:ring-red-500/20 focus:border-red-400 outline-none transition"
              disabled={loading}
            />
          </div>
        </div>
        <div className="mt-4 flex justify-end">
          <button
            onClick={saveGlobal}
            disabled={saving || loading}
            className="px-4 py-2 bg-red-600 text-white text-sm font-medium rounded-lg hover:bg-red-700 disabled:opacity-50 transition"
          >
            {saving ? '保存中...' : '保存全局设置'}
          </button>
        </div>
      </div>

      {/* 用户额度表格 */}
      <div className="bg-white rounded-xl border border-gray-100 shadow-sm overflow-hidden">
        <div className="px-6 py-4 border-b border-gray-100">
          <h3 className="text-sm font-semibold text-gray-700 uppercase tracking-wide">用户额度覆盖</h3>
          <p className="text-xs text-gray-400 mt-0.5">这些用户的额度优先于全局上限生效</p>
        </div>
        <table className="w-full">
          <thead>
            <tr className="bg-gray-50/80 text-xs text-gray-500 uppercase tracking-wide">
              <th className="text-left px-6 py-3 font-medium">ITCode</th>
              <th className="text-right px-6 py-3 font-medium">Backend 每日 (USD)</th>
              <th className="text-right px-6 py-3 font-medium">AWS 每日 (USD)</th>
              <th className="text-center px-6 py-3 font-medium">操作</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-50">
            {loading ? (
              Array.from({ length: 3 }).map((_, i) => (
                <tr key={i}>
                  {[100, 80, 80, 60].map((w, j) => (
                    <td key={j} className="px-6 py-3.5">
                      <div className="skeleton h-3.5 rounded" style={{ width: w }} />
                    </td>
                  ))}
                </tr>
              ))
            ) : data?.user_daily_limits.length === 0 ? (
              <tr>
                <td colSpan={4} className="text-center py-8 text-gray-400 text-sm">
                  暂无用户额度覆盖配置
                </td>
              </tr>
            ) : (
              data?.user_daily_limits.map((u, idx) => (
                <tr key={u.itcode} className="hover:bg-gray-50/50 transition-colors">
                  <td className="px-6 py-3.5 text-sm font-medium text-gray-900">{u.itcode}</td>
                  <td className="px-6 py-3.5 text-right">
                    {editIdx === idx ? (
                      <input
                        type="number"
                        value={editBackend}
                        onChange={(e) => setEditBackend(e.target.value)}
                        className="w-28 px-2 py-1 border border-gray-200 rounded text-sm text-right focus:ring-2 focus:ring-red-500/20 focus:border-red-400 outline-none"
                      />
                    ) : (
                      <span className="text-sm text-gray-700">${u.backend_daily_usd}</span>
                    )}
                  </td>
                  <td className="px-6 py-3.5 text-right">
                    {editIdx === idx ? (
                      <input
                        type="number"
                        value={editAws}
                        onChange={(e) => setEditAws(e.target.value)}
                        className="w-28 px-2 py-1 border border-gray-200 rounded text-sm text-right focus:ring-2 focus:ring-red-500/20 focus:border-red-400 outline-none"
                      />
                    ) : (
                      <span className="text-sm text-gray-700">${u.aws_daily_usd}</span>
                    )}
                  </td>
                  <td className="px-6 py-3.5 text-center">
                    {editIdx === idx ? (
                      <div className="flex items-center justify-center gap-2">
                        <button
                          onClick={() => saveUser(idx)}
                          disabled={saving}
                          className="text-xs px-2.5 py-1 bg-red-600 text-white rounded hover:bg-red-700 disabled:opacity-50 transition"
                        >
                          保存
                        </button>
                        <button
                          onClick={() => setEditIdx(null)}
                          className="text-xs px-2.5 py-1 text-gray-500 hover:text-gray-700 transition"
                        >
                          取消
                        </button>
                      </div>
                    ) : (
                      <button
                        onClick={() => startEdit(idx)}
                        className="text-xs px-2.5 py-1 text-red-600 hover:text-red-800 hover:bg-red-50 rounded transition"
                      >
                        编辑
                      </button>
                    )}
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>
    </div>
  )
}
