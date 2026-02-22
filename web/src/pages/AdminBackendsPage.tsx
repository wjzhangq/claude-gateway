import { useEffect, useState } from 'react'
import { adminGetBackendStats } from '../api'

interface BackendStat {
  backend: string
  requests: number
  total_tokens: number
  cost_usd: number
  avg_latency_ms: number
  error_count: number
}

function toDateStr(d: Date) {
  return d.toISOString().slice(0, 10)
}

export default function AdminBackendsPage() {
  const [date, setDate] = useState(() => toDateStr(new Date()))
  const [stats, setStats] = useState<BackendStat[]>([])
  const [loading, setLoading] = useState(true)

  const load = (d: string) => {
    setLoading(true)
    adminGetBackendStats({ start_date: d, end_date: d })
      .then((res) => setStats(res.data.stats || []))
      .finally(() => setLoading(false))
  }

  useEffect(() => { load(date) }, [date])

  const shiftDate = (days: number) => {
    setDate((d) => toDateStr(new Date(new Date(d).getTime() + days * 86400000)))
  }

  const isToday = date === toDateStr(new Date())
  const totalRequests = stats.reduce((s, b) => s + b.requests, 0)
  const totalTokens = stats.reduce((s, b) => s + b.total_tokens, 0)
  const totalCost = stats.reduce((s, b) => s + b.cost_usd, 0)

  return (
    <div className="p-8">
      <div className="flex items-center gap-4 mb-6">
        <h2 className="text-xl font-semibold text-gray-900">Backend 使用统计</h2>
        <div className="flex items-center gap-1 ml-auto">
          <button
            onClick={() => shiftDate(-1)}
            className="px-2 py-1 text-sm border border-gray-300 rounded hover:bg-gray-50"
          >
            ‹
          </button>
          <input
            type="date"
            value={date}
            onChange={(e) => setDate(e.target.value)}
            className="px-3 py-1 border border-gray-300 rounded text-sm focus:outline-none focus:ring-2 focus:ring-red-500"
          />
          <button
            onClick={() => shiftDate(1)}
            disabled={isToday}
            className="px-2 py-1 text-sm border border-gray-300 rounded hover:bg-gray-50 disabled:opacity-40"
          >
            ›
          </button>
        </div>
      </div>

      {!loading && stats.length > 0 && (
        <div className="grid grid-cols-3 gap-4 mb-6">
          <div className="bg-white rounded-lg border border-gray-200 px-6 py-4">
            <p className="text-xs text-gray-500">总请求数</p>
            <p className="text-2xl font-semibold text-gray-900 mt-1">{totalRequests.toLocaleString()}</p>
          </div>
          <div className="bg-white rounded-lg border border-gray-200 px-6 py-4">
            <p className="text-xs text-gray-500">总 Token</p>
            <p className="text-2xl font-semibold text-gray-900 mt-1">{totalTokens.toLocaleString()}</p>
          </div>
          <div className="bg-white rounded-lg border border-gray-200 px-6 py-4">
            <p className="text-xs text-gray-500">总费用</p>
            <p className="text-2xl font-semibold text-gray-900 mt-1">${totalCost.toFixed(4)}</p>
          </div>
        </div>
      )}

      <div className="bg-white rounded-lg border border-gray-200">
        {loading ? (
          <div className="p-6 text-sm text-gray-400">加载中...</div>
        ) : stats.length === 0 ? (
          <div className="p-6 text-sm text-gray-400">当天暂无数据</div>
        ) : (
          <table className="w-full text-sm">
            <thead className="bg-gray-50">
              <tr>
                {['Backend', '请求数', '占比', '总 Token', '费用', '平均延迟', '错误数'].map((h) => (
                  <th key={h} className="px-4 py-3 text-left text-xs font-medium text-gray-500">
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100">
              {stats.map((s) => {
                const pct = totalRequests > 0 ? ((s.requests / totalRequests) * 100).toFixed(1) : '0'
                return (
                  <tr key={s.backend}>
                    <td className="px-4 py-3 font-mono text-xs font-medium">{s.backend || '—'}</td>
                    <td className="px-4 py-3 font-medium">{s.requests.toLocaleString()}</td>
                    <td className="px-4 py-3">
                      <div className="flex items-center gap-2">
                        <div className="w-20 bg-gray-100 rounded-full h-1.5">
                          <div
                            className="bg-red-500 h-1.5 rounded-full"
                            style={{ width: `${pct}%` }}
                          />
                        </div>
                        <span className="text-gray-500 text-xs">{pct}%</span>
                      </div>
                    </td>
                    <td className="px-4 py-3">{s.total_tokens.toLocaleString()}</td>
                    <td className="px-4 py-3">${s.cost_usd.toFixed(4)}</td>
                    <td className="px-4 py-3 text-gray-500">{Math.round(s.avg_latency_ms)} ms</td>
                    <td className="px-4 py-3">
                      {s.error_count > 0 ? (
                        <span className="px-2 py-0.5 rounded text-xs bg-red-50 text-red-700">
                          {s.error_count}
                        </span>
                      ) : (
                        <span className="text-gray-400">0</span>
                      )}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        )}
      </div>
    </div>
  )
}
