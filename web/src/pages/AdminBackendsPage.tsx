import { useEffect, useState } from 'react'
import { adminGetBackendStats, adminGetBackendStatus } from '../api'
import { toDateStr } from '../utils/time'

interface BackendStat {
  backend: string
  requests: number
  total_tokens: number
  cost_usd: number
  avg_latency_ms: number
  error_count: number
}

interface BackendStatus {
  name: string
  url: string
  weight: number
  disabled: boolean
  err_count: number
  status_codes: number[]
  error_rate: number
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
  const [backendStatus, setBackendStatus] = useState<BackendStatus[]>([])
  const [statusLoading, setStatusLoading] = useState(true)

  const load = (d: string) => {
    setLoading(true)
    adminGetBackendStats({ start_date: d, end_date: d })
      .then((res) => setStats(res.data.stats || []))
      .catch(() => {})
      .finally(() => setLoading(false))
  }

  const loadStatus = () => {
    setStatusLoading(true)
    adminGetBackendStatus()
      .then((res) => setBackendStatus(res.data || []))
      .catch(() => {})
      .finally(() => setStatusLoading(false))
  }

  useEffect(() => { load(date) }, [date])
  useEffect(() => { loadStatus() }, [])
  useEffect(() => {
    const interval = setInterval(loadStatus, 5000)
    return () => clearInterval(interval)
  }, [])

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

      {/* Real-time backend status */}
      <div className="bg-white rounded-xl border border-gray-100 shadow-sm overflow-hidden mb-6">
        <div className="px-4 py-3 border-b border-gray-50 flex items-center justify-between">
          <h3 className="text-sm font-semibold text-gray-700">实时状态 (最近10次)</h3>
          <span className="text-xs text-gray-400">每5秒刷新</span>
        </div>
        <div className="divide-y divide-gray-50">
          {statusLoading ? (
            <div className="px-4 py-6 text-center text-sm text-gray-400">加载中...</div>
          ) : backendStatus.length === 0 ? (
            <div className="px-4 py-6 text-center text-sm text-gray-400">暂无后端</div>
          ) : (
            backendStatus.map((b) => (
              <div key={b.name} className="px-4 py-3 flex items-center gap-4">
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="font-mono text-sm font-semibold text-gray-700">{b.name}</span>
                    {b.disabled ? (
                      <span className="inline-flex items-center px-1.5 py-0.5 rounded text-xs font-medium bg-gray-100 text-gray-500">已禁用</span>
                    ) : (
                      <span className="inline-flex items-center px-1.5 py-0.5 rounded text-xs font-medium bg-green-50 text-green-700">正常</span>
                    )}
                  </div>
                  <div className="text-xs text-gray-400 truncate">{b.url}</div>
                </div>
                <div className="flex items-center gap-2">
                  <span className="text-xs text-gray-500">权重: {b.weight}</span>
                </div>
                <div className="flex items-center gap-1">
                  {b.status_codes.length > 0 ? (
                    b.status_codes.map((code, i) => (
                      <span
                        key={i}
                        className={`inline-flex items-center justify-center w-8 h-6 text-xs font-mono rounded ${
                          code >= 200 && code < 300
                            ? 'bg-green-50 text-green-700'
                            : code >= 400
                            ? 'bg-red-50 text-red-700'
                            : 'bg-yellow-50 text-yellow-700'
                        }`}
                      >
                        {code}
                      </span>
                    ))
                  ) : (
                    <span className="text-xs text-gray-400">暂无</span>
                  )}
                </div>
                <div className="w-24 text-right">
                  <span className={`text-sm font-medium ${b.error_rate > 0 ? 'text-red-600' : 'text-green-600'}`}>
                    {b.error_rate.toFixed(0)}%
                  </span>
                  <span className="text-xs text-gray-400 ml-1">非2xx</span>
                </div>
              </div>
            ))
          )}
        </div>
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
