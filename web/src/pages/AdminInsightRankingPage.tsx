import { useState, useEffect, useCallback } from 'react'
import { useNavigate } from 'react-router-dom'
import { adminGetInsightRanking, type InsightRankingUser } from '../api'

function formatTokens(n: number): string {
  if (n >= 1_000_000_000) return (n / 1_000_000_000).toFixed(2) + 'B'
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(0) + 'K'
  return n.toString()
}

function formatCost(n: number): string {
  return '$' + n.toLocaleString('en-US', { maximumFractionDigits: 2 })
}

const DAYS_OPTIONS = [
  { value: 0, label: '全量' },
  { value: 7, label: '近 7 天' },
  { value: 30, label: '近 30 天' },
  { value: 90, label: '近 90 天' },
]

export default function AdminInsightRankingPage() {
  const navigate = useNavigate()
  const [users, setUsers] = useState<InsightRankingUser[]>([])
  const [total, setTotal] = useState(0)
  const [registeredTotal, setRegisteredTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [search, setSearch] = useState('')
  const [days, setDays] = useState(0)
  const [sortKey, setSortKey] = useState<'all_tokens' | 'total_cost' | 'total_requests'>('all_tokens')
  const [dataUpdatedAt, setDataUpdatedAt] = useState<string>('')

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const { data } = await adminGetInsightRanking({ days, limit: 500 })
      setUsers(data.ranking || [])
      setTotal(data.total_users)
      setRegisteredTotal(data.registered_total)
      setDataUpdatedAt(data.data_updated_at)
    } finally {
      setLoading(false)
    }
  }, [days])

  useEffect(() => { load() }, [load])

  const sorted = [...users]
    .filter(u => !search || u.itcode.includes(search) || (u.name || '').includes(search))
    .sort((a, b) => (b[sortKey] as number) - (a[sortKey] as number))

  return (
    <div className="p-6 max-w-[1200px] mx-auto">
      <h1 className="text-xl font-semibold text-gray-900 mb-1">用量排名</h1>
      <p className="text-sm text-gray-400 mb-5">
        全量用户 Token / 费用排名（backend + aws 合并）· 数据截止 <b className="text-gray-600">{dataUpdatedAt || '—'}</b>
      </p>

      <div className="flex gap-3 items-center mb-4 flex-wrap">
        <input
          placeholder="搜索用户 (itcode / name)"
          value={search}
          onChange={e => setSearch(e.target.value)}
          className="px-3 py-1.5 border border-gray-200 rounded-lg text-sm w-56 focus:outline-none focus:border-red-400"
        />
        <select
          value={days}
          onChange={e => setDays(Number(e.target.value))}
          className="px-3 py-1.5 border border-gray-200 rounded-lg text-sm focus:outline-none focus:border-red-400"
        >
          {DAYS_OPTIONS.map(o => <option key={o.value} value={o.value}>{o.label}</option>)}
        </select>
        <select
          value={sortKey}
          onChange={e => setSortKey(e.target.value as typeof sortKey)}
          className="px-3 py-1.5 border border-gray-200 rounded-lg text-sm focus:outline-none focus:border-red-400"
        >
          <option value="all_tokens">按 Token 总量</option>
          <option value="total_cost">按费用</option>
          <option value="total_requests">按请求次数</option>
        </select>
        <span className="text-sm text-gray-400 ml-auto">
          {search
            ? `匹配 ${sorted.length} / 活跃 ${total} / 注册 ${registeredTotal}`
            : `活跃 ${total} / 注册 ${registeredTotal} 用户`}
        </span>
      </div>

      <div className="bg-white rounded-xl shadow-sm overflow-hidden">
        <table className="w-full text-sm">
          <thead>
            <tr className="bg-gray-50 text-gray-500 text-xs uppercase tracking-wide">
              <th className="px-4 py-3 text-left w-10">#</th>
              <th className="px-4 py-3 text-left">用户</th>
              <th className="px-4 py-3 text-left">渠道</th>
              <th className="px-4 py-3 text-right">Token 总量</th>
              <th className="px-4 py-3 text-right">Backend</th>
              <th className="px-4 py-3 text-right">AWS</th>
              <th className="px-4 py-3 text-right">总费用</th>
              <th className="px-4 py-3 text-right">请求</th>
              <th className="px-4 py-3 text-left">标签</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-50">
            {loading ? (
              <tr><td colSpan={9} className="px-4 py-10 text-center text-gray-400">加载中…</td></tr>
            ) : sorted.length === 0 ? (
              <tr><td colSpan={9} className="px-4 py-10 text-center text-gray-400">暂无数据</td></tr>
            ) : sorted.map((u, i) => (
              <tr key={u.user_id} className="hover:bg-gray-50/60">
                <td className="px-4 py-3 text-gray-400 text-xs">{i + 1}</td>
                <td className="px-4 py-3">
                  <button
                    onClick={() => navigate(`/admin/insight/user/${u.user_id}`)}
                    className="font-medium text-red-700 hover:underline text-left"
                  >
                    {u.name || u.itcode}
                  </button>
                  <div className="text-[11px] text-gray-400">{u.itcode}</div>
                </td>
                <td className="px-4 py-3">
                  {u.channels.map(c => (
                    <span
                      key={c}
                      className={`inline-block px-1.5 py-0.5 rounded text-[10px] font-medium mr-1 ${
                        c === 'aws' ? 'bg-orange-100 text-orange-700' : 'bg-blue-100 text-blue-700'
                      }`}
                    >{c}</span>
                  ))}
                </td>
                <td className="px-4 py-3 text-right font-semibold">{formatTokens(u.all_tokens)}</td>
                <td className="px-4 py-3 text-right text-gray-600">{formatTokens(u.backend_tokens)}</td>
                <td className="px-4 py-3 text-right text-gray-600">{u.aws_tokens > 0 ? formatTokens(u.aws_tokens) : '-'}</td>
                <td className="px-4 py-3 text-right text-red-600">{formatCost(u.total_cost)}</td>
                <td className="px-4 py-3 text-right text-gray-600">{u.total_requests.toLocaleString()}</td>
                <td className="px-4 py-3">
                  <span className={`inline-block px-1.5 py-0.5 rounded text-[10px] font-medium ${
                    u.role_tag === '研发' ? 'bg-green-100 text-green-700'
                      : u.role_tag === '非研发' ? 'bg-orange-100 text-orange-700'
                      : 'bg-gray-100 text-gray-500'
                  }`}>{u.role_tag}</span>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}
