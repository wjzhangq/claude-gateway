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

function SkeletonCard() {
  return (
    <div className="bg-white rounded-xl border border-gray-100 px-6 py-5 shadow-sm">
      <div className="skeleton h-3 w-16 rounded mb-3" />
      <div className="skeleton h-7 w-24 rounded" />
    </div>
  )
}

function SkeletonRow() {
  return (
    <tr>
      {[100, 70, 120, 80, 70, 80, 60].map((w, i) => (
        <td key={i} className="px-4 py-3.5">
          <div className="skeleton h-3.5 rounded" style={{ width: w }} />
        </td>
      ))}
    </tr>
  )
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
      <div className="flex items-center gap-4 mb-7">
        <div>
          <h2 className="text-xl font-bold text-gray-900">Backend 统计</h2>
          <p className="text-sm text-gray-400 mt-0.5">各后端服务使用情况</p>
        </div>
        <div className="flex items-center gap-1.5 ml-auto bg-white border border-gray-200 rounded-xl px-2 py-1.5 shadow-sm">
          <button
            onClick={() => shiftDate(-1)}
            className="w-7 h-7 flex items-center justify-center rounded-lg text-gray-500 hover:bg-gray-100 transition-colors text-sm font-medium"
          >
            ‹
          </button>
          <input
            type="date"
            value={date}
            onChange={(e) => setDate(e.target.value)}
            className="px-2 py-0.5 text-sm text-gray-700 focus:outline-none bg-transparent"
          />
          <button
            onClick={() => shiftDate(1)}
            disabled={isToday}
            className="w-7 h-7 flex items-center justify-center rounded-lg text-gray-500 hover:bg-gray-100 disabled:opacity-30 transition-colors text-sm font-medium"
          >
            ›
          </button>
        </div>
      </div>

      {/* Summary cards */}
      <div className="grid grid-cols-3 gap-4 mb-6">
        {loading ? (
          <>
            <SkeletonCard />
            <SkeletonCard />
            <SkeletonCard />
          </>
        ) : stats.length > 0 ? (
          <>
            <div className="bg-white rounded-xl border border-gray-100 px-6 py-5 shadow-sm hover:shadow-md transition-shadow">
              <div className="inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium bg-blue-50 text-blue-600 mb-3">总请求数</div>
              <p className="text-2xl font-bold text-gray-900">{totalRequests.toLocaleString()}</p>
            </div>
            <div className="bg-white rounded-xl border border-gray-100 px-6 py-5 shadow-sm hover:shadow-md transition-shadow">
              <div className="inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium bg-purple-50 text-purple-600 mb-3">总 Token</div>
              <p className="text-2xl font-bold text-gray-900">{totalTokens.toLocaleString()}</p>
            </div>
            <div className="bg-white rounded-xl border border-gray-100 px-6 py-5 shadow-sm hover:shadow-md transition-shadow">
              <div className="inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium bg-red-50 text-red-600 mb-3">总费用</div>
              <p className="text-2xl font-bold text-gray-900">${totalCost.toFixed(4)}</p>
            </div>
          </>
        ) : null}
      </div>

      <div className="bg-white rounded-xl border border-gray-100 shadow-sm overflow-hidden">
        <table className="w-full text-sm">
          <thead className="bg-gray-50/80">
            <tr>
              {['Backend', '请求数', '占比', '总 Token', '费用', '平均延迟', '错误数'].map((h) => (
                <th key={h} className="px-4 py-3 text-left text-xs font-semibold text-gray-400 uppercase tracking-wide">
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-50">
            {loading ? (
              Array.from({ length: 3 }).map((_, i) => <SkeletonRow key={i} />)
            ) : stats.length === 0 ? (
              <tr>
                <td colSpan={7} className="px-4 py-10 text-center text-sm text-gray-400">当天暂无数据</td>
              </tr>
            ) : (
              stats.map((s) => {
                const pct = totalRequests > 0 ? ((s.requests / totalRequests) * 100).toFixed(1) : '0'
                return (
                  <tr key={s.backend} className="hover:bg-gray-50/50 transition-colors">
                    <td className="px-4 py-3.5 font-mono text-xs font-semibold text-gray-700">{s.backend || '—'}</td>
                    <td className="px-4 py-3.5 font-medium text-gray-800">{s.requests.toLocaleString()}</td>
                    <td className="px-4 py-3.5">
                      <div className="flex items-center gap-2">
                        <div className="w-20 bg-gray-100 rounded-full h-1.5 overflow-hidden">
                          <div
                            className="bg-red-500 h-1.5 rounded-full transition-all"
                            style={{ width: `${pct}%` }}
                          />
                        </div>
                        <span className="text-gray-500 text-xs tabular-nums">{pct}%</span>
                      </div>
                    </td>
                    <td className="px-4 py-3.5 text-gray-700">{s.total_tokens.toLocaleString()}</td>
                    <td className="px-4 py-3.5 text-gray-700">${s.cost_usd.toFixed(4)}</td>
                    <td className="px-4 py-3.5 text-gray-500 tabular-nums">{Math.round(s.avg_latency_ms)} ms</td>
                    <td className="px-4 py-3.5">
                      {s.error_count > 0 ? (
                        <span className="inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium bg-red-50 text-red-700 ring-1 ring-red-100">
                          {s.error_count}
                        </span>
                      ) : (
                        <span className="text-gray-300 text-xs">0</span>
                      )}
                    </td>
                  </tr>
                )
              })
            )}
          </tbody>
        </table>
      </div>
    </div>
  )
}
