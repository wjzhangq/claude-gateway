import { useEffect, useState } from 'react'
import { adminGetBackendStats } from '../api'
import { toDateStr } from '../utils/time'

interface BackendStat {
  backend: string
  requests: number
  total_tokens: number
  cost_usd: number
  avg_latency_ms: number
  error_count: number
}

function SkeletonRow() {
  return (
    <tr>
      {[120, 80, 100, 80, 70, 60].map((w, i) => (
        <td key={i} className="px-4 py-3.5">
          <div className="skeleton h-3.5 rounded" style={{ width: w }} />
        </td>
      ))}
    </tr>
  )
}

export default function AdminPublicStatsPage() {
  const [date, setDate] = useState(() => toDateStr(new Date()))
  const [stats, setStats] = useState<BackendStat[]>([])
  const [loading, setLoading] = useState(true)

  // Load stats for the selected date
  const load = (d: string) => {
    setLoading(true)
    adminGetBackendStats({ start_date: d, end_date: d })
      .then((res) => {
        const all: BackendStat[] = res.data.stats || []
        setStats(all.filter((s) => s.backend.startsWith('public:')))
      })
      .catch(() => {})
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

  // Provider breakdown: strip "public:" prefix for display
  const providerStats = stats.map((s) => ({
    ...s,
    provider: s.backend.replace('public:', ''),
  }))

  return (
    <div className="p-8">
      <div className="flex items-center gap-4 mb-7">
        <div>
          <h2 className="text-xl font-bold text-gray-900">Public Model Stats</h2>
          <p className="text-sm text-gray-400 mt-0.5">Kimi / MiniMax etc. provider statistics</p>
        </div>
        <div className="flex items-center gap-1.5 ml-auto bg-white border border-gray-200 rounded-xl px-2 py-1.5 shadow-sm">
          <button
            onClick={() => shiftDate(-1)}
            className="w-7 h-7 flex items-center justify-center rounded-lg text-gray-500 hover:bg-gray-100 transition-colors text-sm font-medium"
          >
            &lsaquo;
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
            &rsaquo;
          </button>
        </div>
      </div>

      {/* Summary cards */}
      <div className="grid grid-cols-3 gap-4 mb-6">
        {loading ? (
          Array.from({ length: 3 }).map((_, i) => (
            <div key={i} className="bg-white rounded-xl border border-gray-100 px-6 py-5 shadow-sm">
              <div className="skeleton h-3 w-16 rounded mb-3" />
              <div className="skeleton h-7 w-24 rounded" />
            </div>
          ))
        ) : (
          <>
            <div className="bg-white rounded-xl border border-gray-100 px-6 py-5 shadow-sm hover:shadow-md transition-shadow">
              <div className="inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium bg-blue-50 text-blue-600 mb-3">Requests</div>
              <p className="text-2xl font-bold text-gray-900">{totalRequests.toLocaleString()}</p>
            </div>
            <div className="bg-white rounded-xl border border-gray-100 px-6 py-5 shadow-sm hover:shadow-md transition-shadow">
              <div className="inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium bg-purple-50 text-purple-600 mb-3">Tokens</div>
              <p className="text-2xl font-bold text-gray-900">{totalTokens.toLocaleString()}</p>
            </div>
            <div className="bg-white rounded-xl border border-gray-100 px-6 py-5 shadow-sm hover:shadow-md transition-shadow">
              <div className="inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium bg-red-50 text-red-600 mb-3">Cost</div>
              <p className="text-2xl font-bold text-gray-900">${totalCost.toFixed(4)}</p>
            </div>
          </>
        )}
      </div>

      {/* Per-provider table */}
      <div className="bg-white rounded-xl border border-gray-100 shadow-sm overflow-hidden">
        <div className="px-6 py-4 border-b border-gray-100">
          <h3 className="text-sm font-semibold text-gray-700">{date} Provider Breakdown</h3>
        </div>
        <table className="w-full text-sm">
          <thead className="bg-gray-50/80">
            <tr>
              {['Provider', 'Requests', 'Tokens', 'Cost', 'Avg Latency', 'Errors'].map((h) => (
                <th key={h} className="px-4 py-3 text-left text-xs font-semibold text-gray-400 uppercase tracking-wide">{h}</th>
              ))}
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-50">
            {loading ? (
              Array.from({ length: 2 }).map((_, i) => <SkeletonRow key={i} />)
            ) : providerStats.length === 0 ? (
              <tr>
                <td colSpan={6} className="px-4 py-10 text-center text-sm text-gray-400">No public provider data for this date</td>
              </tr>
            ) : (
              providerStats.map((s) => {
                const pct = totalRequests > 0 ? ((s.requests / totalRequests) * 100).toFixed(1) : '0'
                return (
                  <tr key={s.backend} className="hover:bg-gray-50/50 transition-colors">
                    <td className="px-4 py-3.5">
                      <span className="inline-flex items-center px-2.5 py-1 rounded-lg text-xs font-semibold bg-indigo-50 text-indigo-700 ring-1 ring-indigo-100">
                        {s.provider}
                      </span>
                    </td>
                    <td className="px-4 py-3.5">
                      <div className="flex items-center gap-2">
                        <span className="font-medium text-gray-800">{s.requests.toLocaleString()}</span>
                        <span className="text-xs text-gray-400">{pct}%</span>
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
