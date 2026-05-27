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
  status_code_dist: Record<number, number>
  status_codes: number[]
  error_rate: number
  quota_exhausted: boolean
  quota_limit: number
  quota_usage: number
  quota_checked_at: number
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

  const loadStatus = (silent?: boolean) => {
    if (!silent) setStatusLoading(true)
    adminGetBackendStatus()
      .then((res) => setBackendStatus(res.data || []))
      .catch(() => {})
      .finally(() => { if (!silent) setStatusLoading(false) })
  }

  useEffect(() => { load(date) }, [date])
  useEffect(() => { loadStatus() }, [])
  useEffect(() => {
    const interval = setInterval(() => loadStatus(true), 5000)
    return () => clearInterval(interval)
  }, [])

  const shiftDate = (days: number) => {
    setDate((d) => toDateStr(new Date(new Date(d).getTime() + days * 86400000)))
  }

  const isToday = date === toDateStr(new Date())
  const filteredStats = stats.filter((s) => !s.backend.startsWith('public'))
  const totalRequests = filteredStats.reduce((s, b) => s + b.requests, 0)
  const totalTokens = filteredStats.reduce((s, b) => s + b.total_tokens, 0)
  const totalCost = filteredStats.reduce((s, b) => s + b.cost_usd, 0)
  const DAILY_LIMIT = 200

  // Aggregate status code distribution from all backends
  const combinedStatusDist = backendStatus.reduce((acc, b) => {
    for (const [code, count] of Object.entries(b.status_code_dist || {})) {
      const c = parseInt(code, 10)
      acc[c] = (acc[c] || 0) + count
    }
    return acc
  }, {} as Record<number, number>)

  const totalStatusCodes = Object.values(combinedStatusDist).reduce((s, c) => s + c, 0)

  // Merge all backends' status_codes into a single ordered list (last 50)
  const allStatusCodes: number[] = backendStatus.flatMap(b => b.status_codes || [])
  // Take last 50
  const recentCodes = allStatusCodes.slice(-50)

  // Status code colors and labels
  const getStatusInfo = (code: number) => {
    if (code === 200) return { label: '200 正常', color: 'bg-green-500', dotColor: '#22c55e', bg: 'bg-green-50', text: 'text-green-700' }
    if (code === 419) return { label: '419 额度超', color: 'bg-orange-500', dotColor: '#f97316', bg: 'bg-orange-50', text: 'text-orange-700' }
    if (code === 503) return { label: '503 服务异常', color: 'bg-red-500', dotColor: '#ef4444', bg: 'bg-red-50', text: 'text-red-700' }
    if (code >= 200 && code < 300) return { label: `${code}`, color: 'bg-green-400', dotColor: '#4ade80', bg: 'bg-green-50', text: 'text-green-600' }
    if (code >= 400 && code < 500) return { label: `${code}`, color: 'bg-orange-400', dotColor: '#fb923c', bg: 'bg-orange-50', text: 'text-orange-600' }
    if (code >= 500) return { label: `${code}`, color: 'bg-red-400', dotColor: '#f87171', bg: 'bg-red-50', text: 'text-red-600' }
    return { label: `${code}`, color: 'bg-gray-400', dotColor: '#9ca3af', bg: 'bg-gray-50', text: 'text-gray-600' }
  }

  const pieData = Object.entries(combinedStatusDist)
    .map(([code, count]) => ({
      code: parseInt(code, 10),
      count,
      pct: totalStatusCodes > 0 ? (count / totalStatusCodes) * 100 : 0,
      ...getStatusInfo(parseInt(code, 10))
    }))
    .sort((a, b) => b.count - a.count)

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

      {/* HTTP Status Code Distribution - Last 50 requests */}
      <div className="bg-white rounded-xl border border-gray-100 shadow-sm overflow-hidden mb-6">
        <div className="px-4 py-3 border-b border-gray-50 flex items-center justify-between">
          <h3 className="text-sm font-semibold text-gray-700">HTTP 状态码分布 (最近50请求)</h3>
          <span className="text-xs text-gray-400">每5秒刷新</span>
        </div>
        <div className="p-5">
          {statusLoading ? (
            <div className="text-center text-sm text-gray-400">加载中...</div>
          ) : recentCodes.length === 0 ? (
            <div className="text-center text-sm text-gray-400">暂无请求数据</div>
          ) : (
            <div>
              {/* 50-slot strip */}
              <div className="flex gap-1 mb-4">
                {Array.from({ length: 50 }).map((_, i) => {
                  const code = recentCodes[i]
                  if (code == null) {
                    return (
                      <div
                        key={i}
                        className="flex-1 h-7 rounded-sm bg-gray-100"
                        title="—"
                      />
                    )
                  }
                  const info = getStatusInfo(code)
                  return (
                    <div
                      key={i}
                      className={`flex-1 h-7 rounded-sm ${info.color} cursor-default transition-opacity hover:opacity-75`}
                      title={`#${i + 1} ${info.label}`}
                    />
                  )
                })}
              </div>
              {/* Legend */}
              <div className="flex flex-wrap gap-x-5 gap-y-1.5">
                {pieData.map((d) => (
                  <div key={d.code} className="flex items-center gap-1.5">
                    <span className={`w-2.5 h-2.5 rounded-sm ${d.color}`} />
                    <span className={`text-xs font-medium ${d.text}`}>{d.label}</span>
                    <span className="text-xs text-gray-400 tabular-nums">{d.count} ({d.pct.toFixed(1)}%)</span>
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>
      </div>

      {/* Backend Status */}
      <div className="bg-white rounded-xl border border-gray-100 shadow-sm overflow-hidden mb-6">
        <div className="px-4 py-3 border-b border-gray-50">
          <h3 className="text-sm font-semibold text-gray-700">Backend 状态</h3>
        </div>
        <table className="w-full text-sm">
          <thead className="bg-gray-50/80">
            <tr>
              {['Backend', '状态', '权重', '额度用量', '最近检查'].map((h) => (
                <th key={h} className="px-4 py-3 text-left text-xs font-semibold text-gray-400 uppercase tracking-wide">
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-50">
            {statusLoading ? (
              Array.from({ length: 3 }).map((_, i) => (
                <tr key={i}>
                  {Array.from({ length: 5 }).map((_, j) => (
                    <td key={j} className="px-4 py-3.5"><div className="skeleton h-3.5 w-20 rounded" /></td>
                  ))}
                </tr>
              ))
            ) : backendStatus.length === 0 ? (
              <tr><td colSpan={5} className="px-4 py-10 text-center text-sm text-gray-400">无数据</td></tr>
            ) : (
              backendStatus.map((b) => {
                const isHealthy = !b.disabled && !b.quota_exhausted
                const statusLabel = b.disabled ? '已禁用' : b.quota_exhausted ? '额度耗尽' : '正常'
                const statusColor = b.disabled ? 'bg-red-50 text-red-700 ring-red-100' : b.quota_exhausted ? 'bg-orange-50 text-orange-700 ring-orange-100' : 'bg-green-50 text-green-700 ring-green-100'
                const quotaStr = b.quota_limit > 0
                  ? `$${b.quota_usage.toFixed(2)} / $${b.quota_limit.toFixed(2)}`
                  : '—'
                const quotaPct = b.quota_limit > 0 ? (b.quota_usage / b.quota_limit) * 100 : 0
                const checkedStr = b.quota_checked_at > 0
                  ? new Date(b.quota_checked_at * 1000).toLocaleTimeString()
                  : '—'
                return (
                  <tr key={b.name} className="hover:bg-gray-50/50 transition-colors">
                    <td className="px-4 py-3.5 font-mono text-xs font-semibold text-gray-700">{b.name}</td>
                    <td className="px-4 py-3.5">
                      <span className={`inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium ring-1 ${statusColor}`}>
                        <span className={`w-1.5 h-1.5 rounded-full mr-1.5 ${isHealthy ? 'bg-green-500' : b.disabled ? 'bg-red-500' : 'bg-orange-500'}`} />
                        {statusLabel}
                      </span>
                    </td>
                    <td className="px-4 py-3.5 text-gray-700">{b.weight}</td>
                    <td className="px-4 py-3.5">
                      {b.quota_limit > 0 ? (
                        <div className="flex items-center gap-2">
                          <div className="w-24 bg-gray-100 rounded-full h-1.5 overflow-hidden">
                            <div
                              className={`h-1.5 rounded-full transition-all ${quotaPct >= 100 ? 'bg-red-500' : quotaPct >= 80 ? 'bg-orange-500' : 'bg-green-500'}`}
                              style={{ width: `${Math.min(quotaPct, 100)}%` }}
                            />
                          </div>
                          <span className="text-xs text-gray-500 tabular-nums">{quotaStr}</span>
                        </div>
                      ) : (
                        <span className="text-xs text-gray-300">—</span>
                      )}
                    </td>
                    <td className="px-4 py-3.5 text-xs text-gray-400">{checkedStr}</td>
                  </tr>
                )
              })
            )}
          </tbody>
        </table>
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
            ) : filteredStats.length === 0 ? (
              <tr>
                <td colSpan={7} className="px-4 py-10 text-center text-sm text-gray-400">当天暂无数据</td>
              </tr>
            ) : (
              filteredStats.map((s) => {
                const pct = totalRequests > 0 ? ((s.requests / totalRequests) * 100).toFixed(1) : '0'
                const isDasheng = s.backend.startsWith('dasheng-')
                const costPct = ((s.cost_usd / DAILY_LIMIT) * 100).toFixed(1)
                return (
                  <tr key={s.backend} className="hover:bg-gray-50/50 transition-colors">
                    <td className="px-4 py-3.5 font-mono text-xs font-semibold text-gray-700">
                      {s.backend || '—'}
                      {isDasheng && (
                        <span className="ml-2 inline-flex items-center px-1.5 py-0.5 rounded text-[10px] font-semibold bg-indigo-50 text-indigo-600 ring-1 ring-indigo-100">大圣</span>
                      )}
                    </td>
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
                    <td className="px-4 py-3.5 text-gray-700">
                      ${s.cost_usd.toFixed(4)}
                      <span className={`ml-1.5 text-xs ${parseFloat(costPct) >= 80 ? 'text-red-500 font-medium' : 'text-gray-400'}`}>({costPct}%)</span>
                    </td>
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
