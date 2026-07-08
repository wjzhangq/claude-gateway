import { Fragment, useEffect, useState } from 'react'
import { adminGetBackendStats, adminGetBackendStatus } from '../api'
import { toDateStr } from '../utils/time'

interface StatusEntry {
  code: number
  latency_ms: number
  timestamp: number // unix seconds
}

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
  state: 'healthy' | 'degraded' | 'disabled'
  effective_weight: number
  disabled: boolean
  err_count: number
  status_code_dist: Record<number, number>
  status_entries: StatusEntry[]
  recent_latency_ms: number
  error_rate: number
  quota_exhausted: boolean
  quota_limit: number
  quota_usage: number
  quota_checked_at: number
}

// Per-backend border accent colors (cycles by index)
const BACKEND_BORDER_COLORS = [
  'border-l-blue-500',
  'border-l-purple-500',
  'border-l-orange-500',
  'border-l-teal-500',
  'border-l-rose-500',
  'border-l-indigo-500',
  'border-l-amber-500',
  'border-l-emerald-500',
]

// Dot colors matching border colors for the legend
const BACKEND_DOT_COLORS = [
  'bg-blue-500',
  'bg-purple-500',
  'bg-orange-500',
  'bg-teal-500',
  'bg-rose-500',
  'bg-indigo-500',
  'bg-amber-500',
  'bg-emerald-500',
]

function getStatusColor(code: number): string {
  if (code >= 200 && code < 300) return 'bg-green-500'
  if (code >= 300 && code < 400) return 'bg-blue-400'
  if (code >= 400 && code < 500) return 'bg-amber-400'
  if (code >= 500) return 'bg-red-500'
  return 'bg-gray-300'
}

function getStatusLabel(code: number): string {
  if (code === 200) return '200 正常'
  if (code === 429) return '429 限速'
  if (code === 403) return '403 禁止'
  if (code === 401) return '401 认证失败'
  if (code === 503) return '503 服务异常'
  if (code >= 200 && code < 300) return `${code} 成功`
  if (code >= 300 && code < 400) return `${code} 重定向`
  if (code >= 400 && code < 500) return `${code} 客户端错误`
  if (code >= 500) return `${code} 服务端错误`
  return `${code}`
}

// Format a duration in seconds as human-readable (e.g. "2m30s", "45s", "3h12m")
function formatDuration(seconds: number): string {
  if (seconds < 60) return `${Math.round(seconds)}s`
  if (seconds < 3600) {
    const m = Math.floor(seconds / 60)
    const s = Math.round(seconds % 60)
    return s > 0 ? `${m}m${s}s` : `${m}m`
  }
  const h = Math.floor(seconds / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  return m > 0 ? `${h}h${m}m` : `${h}h`
}

function SkeletonCard() {
  return (
    <div className="bg-white rounded-xl border border-gray-100 px-6 py-5 shadow-sm">
      <div className="skeleton h-3 w-16 rounded mb-3" />
      <div className="skeleton h-7 w-24 rounded" />
    </div>
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

  // Aggregate status code distribution from all backends (for the global strip)
  const combinedStatusDist = backendStatus.reduce((acc, b) => {
    for (const [code, count] of Object.entries(b.status_code_dist || {})) {
      const c = parseInt(code, 10)
      acc[c] = (acc[c] || 0) + count
    }
    return acc
  }, {} as Record<number, number>)

  const totalStatusCount = Object.values(combinedStatusDist).reduce((s, c) => s + c, 0)

  // Merge all status_entries from all backends, sort by timestamp descending, take last 50
  const allEntries: StatusEntry[] = backendStatus.flatMap(b => b.status_entries || [])
  allEntries.sort((a, b) => a.timestamp - b.timestamp) // oldest first
  const recentEntries = allEntries.slice(-50) // last 50, newest on the right

  // Compute time range from all status_entries across all backends
  const now = Math.floor(Date.now() / 1000)
  let minTs = now
  let maxTs = 0
  for (const b of backendStatus) {
    for (const e of b.status_entries || []) {
      if (e.timestamp > 0) {
        if (e.timestamp < minTs) minTs = e.timestamp
        if (e.timestamp > maxTs) maxTs = e.timestamp
      }
    }
  }
  const hasTimestamps = maxTs > 0
  const timeRangeSeconds = hasTimestamps ? now - minTs : 0

  // Build sorted distribution entries for the legend
  const distEntries = Object.entries(combinedStatusDist)
    .map(([code, count]) => ({
      code: parseInt(code, 10),
      count,
      pct: totalStatusCount > 0 ? (count / totalStatusCount) * 100 : 0,
    }))
    .sort((a, b) => b.count - a.count)

  // Merged list: backendStatus is the source of truth for the table rows
  const backendList = backendStatus.map((bs, index) => ({
    status: bs,
    stat: filteredStats.find((s) => s.backend === bs.name) ?? null,
    borderColor: BACKEND_BORDER_COLORS[index % BACKEND_BORDER_COLORS.length],
    dotColor: BACKEND_DOT_COLORS[index % BACKEND_DOT_COLORS.length],
  }))

  return (
    <div className="p-8">
      {/* Header + date picker */}
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

      {/* HTTP Status Code Distribution strip (global, all backends combined) */}
      <div className="bg-white rounded-xl border border-gray-100 shadow-sm overflow-hidden mb-6">
        <div className="px-4 py-3 border-b border-gray-50 flex items-center justify-between">
          <div className="flex items-center gap-3">
            <h3 className="text-sm font-semibold text-gray-700">HTTP 状态码分布</h3>
            {hasTimestamps && (
              <span className="text-xs text-gray-400">
                最近 {formatDuration(timeRangeSeconds)} · {totalStatusCount} 次请求
              </span>
            )}
          </div>
          <span className="text-xs text-gray-400">每5秒刷新</span>
        </div>
        <div className="p-5">
          {statusLoading ? (
            <div className="text-center text-sm text-gray-400">加载中...</div>
          ) : (
            <div>
              {/* Fixed 50 slots, gray on left if no data, newest on right */}
              <div className="flex gap-1 mb-4">
                {Array.from({ length: 50 }).map((_, i) => {
                  const offset = 50 - recentEntries.length
                  const entry = i >= offset ? recentEntries[i - offset] : undefined
                  if (!entry) {
                    return (
                      <div
                        key={i}
                        className="w-5 h-5 rounded-sm bg-gray-100"
                        title="—"
                      />
                    )
                  }
                  return (
                    <div
                      key={i}
                      className={`w-5 h-5 rounded-sm ${getStatusColor(entry.code)} opacity-90 hover:opacity-100 cursor-default transition-opacity`}
                      title={`${getStatusLabel(entry.code)} | ${entry.latency_ms > 0 ? `${entry.latency_ms}ms` : '—'}`}
                    />
                  )
                })}
              </div>
              {/* Legend */}
              <div className="flex flex-wrap gap-x-5 gap-y-1.5">
                {distEntries.map((d) => (
                  <div key={d.code} className="flex items-center gap-1.5">
                    <span className={`w-2.5 h-2.5 rounded-sm ${getStatusColor(d.code)}`} />
                    <span className="text-xs font-medium text-gray-600">{getStatusLabel(d.code)}</span>
                    <span className="text-xs text-gray-400 tabular-nums">{d.count} ({d.pct.toFixed(1)}%)</span>
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>
      </div>

      {/* Combined Backend table: each backend = 2 rows */}
      <div className="bg-white rounded-xl border border-gray-100 shadow-sm overflow-hidden">
        <table className="w-full text-sm">
          <thead className="bg-gray-50/80">
            <tr>
              {['Backend', '请求数', '总 Token', '费用', '平均延迟', '最近延迟', '错误数', '权重', '状态', '最近检查'].map((h) => (
                <th key={h} className="px-3 py-3 text-left text-xs font-semibold text-gray-400 uppercase tracking-wide whitespace-nowrap">
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {statusLoading ? (
              Array.from({ length: 3 }).map((_, i) => (
                <Fragment key={i}>
                  <tr>
                    {Array.from({ length: 10 }).map((_, j) => (
                      <td key={j} className="px-3 py-3"><div className="skeleton h-3.5 w-16 rounded" /></td>
                    ))}
                  </tr>
                  <tr className="border-b border-gray-50">
                    <td className="px-3 py-2" />
                    <td colSpan={9} className="px-3 py-2">
                      <div className="flex justify-end gap-0.5">
                        {Array.from({ length: 20 }).map((_, k) => (
                          <div key={k} className="w-5 h-5 rounded-sm skeleton" />
                        ))}
                      </div>
                    </td>
                  </tr>
                </Fragment>
              ))
            ) : backendList.length === 0 ? (
              <tr>
                <td colSpan={10} className="px-4 py-10 text-center text-sm text-gray-400">无数据</td>
              </tr>
            ) : (
              backendList.map(({ status: b, stat, borderColor, dotColor }) => {
                // State badge
                let statusLabel: string
                let statusColor: string
                let statusDot: string
                if (b.quota_exhausted) {
                  statusLabel = '额度耗尽'; statusColor = 'bg-orange-50 text-orange-700 ring-orange-100'; statusDot = 'bg-orange-500'
                } else if (b.state === 'disabled') {
                  statusLabel = '已禁用'; statusColor = 'bg-red-50 text-red-700 ring-red-100'; statusDot = 'bg-red-500'
                } else if (b.state === 'degraded') {
                  statusLabel = '降级中'; statusColor = 'bg-yellow-50 text-yellow-700 ring-yellow-100'; statusDot = 'bg-yellow-500'
                } else {
                  statusLabel = '正常'; statusColor = 'bg-green-50 text-green-700 ring-green-100'; statusDot = 'bg-green-500'
                }

                const checkedStr = b.quota_checked_at > 0
                  ? new Date(b.quota_checked_at * 1000).toLocaleTimeString()
                  : '—'

                const entries = b.status_entries || []

                return (
                  <Fragment key={b.name}>
                    {/* Row 1: stats */}
                    <tr className="hover:bg-gray-50/50 transition-colors border-t border-gray-50">
                      <td className={`px-3 py-3 font-mono text-xs font-semibold text-gray-700 border-l-4 ${borderColor} whitespace-nowrap`}>
                        <div className="flex items-center gap-1.5">
                          <span className={`w-1.5 h-1.5 rounded-full ${dotColor}`} />
                          {b.name}
                        </div>
                      </td>
                      <td className="px-3 py-3 font-medium text-gray-800 tabular-nums">
                        {stat?.requests.toLocaleString() ?? '—'}
                      </td>
                      <td className="px-3 py-3 text-gray-700 tabular-nums">
                        {stat ? stat.total_tokens.toLocaleString() : '—'}
                      </td>
                      <td className="px-3 py-3 text-gray-700 tabular-nums">
                        {stat ? `$${stat.cost_usd.toFixed(4)}` : '—'}
                      </td>
                      <td className="px-3 py-3 text-gray-500 tabular-nums">
                        {stat ? `${Math.round(stat.avg_latency_ms)} ms` : '—'}
                      </td>
                      <td className="px-3 py-3 text-gray-500 tabular-nums">
                        {b.recent_latency_ms > 0 ? `${b.recent_latency_ms} ms` : '—'}
                      </td>
                      <td className="px-3 py-3">
                        {(stat?.error_count ?? 0) > 0 ? (
                          <span className="inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium bg-red-50 text-red-700 ring-1 ring-red-100">
                            {stat!.error_count}
                          </span>
                        ) : (
                          <span className="text-gray-300 text-xs">0</span>
                        )}
                      </td>
                      <td className="px-3 py-3 text-gray-700 tabular-nums">
                        {b.state === 'degraded' ? (
                          <span>{b.effective_weight}<span className="text-xs text-gray-400"> / {b.weight}</span></span>
                        ) : b.weight}
                      </td>
                      <td className="px-3 py-3">
                        <span className={`inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium ring-1 ${statusColor}`}>
                          <span className={`w-1.5 h-1.5 rounded-full mr-1.5 ${statusDot}`} />
                          {statusLabel}
                        </span>
                      </td>
                      <td className="px-3 py-3 text-xs text-gray-400 whitespace-nowrap">{checkedStr}</td>
                    </tr>

                    {/* Row 2: status code squares (right-aligned, time-ordered) */}
                    <tr className="border-b border-gray-100">
                      <td className={`px-3 py-1.5 text-gray-300 text-xs border-l-4 ${borderColor}`}>—</td>
                      <td colSpan={9} className="px-3 py-1.5">
                        {entries.length === 0 ? (
                          <span className="text-xs text-gray-300 float-right">暂无记录</span>
                        ) : (
                          <div className="flex justify-end gap-0.5 flex-wrap">
                            {entries.map((entry, i) => (
                              <div
                                key={i}
                                className={`w-5 h-5 rounded-sm cursor-default opacity-90 hover:opacity-100 transition-opacity ${getStatusColor(entry.code)}`}
                                title={`${b.name} | ${entry.code} | ${entry.latency_ms > 0 ? `${entry.latency_ms}ms` : '—'}`}
                              />
                            ))}
                          </div>
                        )}
                      </td>
                    </tr>
                  </Fragment>
                )
              })
            )}
          </tbody>
        </table>
      </div>
    </div>
  )
}
