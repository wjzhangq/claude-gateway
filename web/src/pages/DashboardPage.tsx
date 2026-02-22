import { useEffect, useState } from 'react'
import { getMyUsage, getMyDailyStats } from '../api'
import {
  BarChart, Bar, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer,
} from 'recharts'

interface UsageLog {
  id: number
  model: string
  input_tokens: number
  output_tokens: number
  total_tokens: number
  cost_usd: number
  status_code: number
  created_at: string
}

interface DailyStat {
  date: string
  cost_usd: number
}

function toDateStr(d: Date) {
  return d.toISOString().slice(0, 10)
}

function SkeletonRow() {
  return (
    <tr>
      {[120, 60, 60, 70, 50, 90].map((w, i) => (
        <td key={i} className="px-4 py-3">
          <div className="skeleton h-3.5 rounded" style={{ width: w }} />
        </td>
      ))}
    </tr>
  )
}

function StatCardSkeleton() {
  return (
    <div className="bg-white rounded-xl border border-gray-100 px-6 py-5 shadow-sm">
      <div className="skeleton h-3 w-24 rounded mb-3" />
      <div className="skeleton h-7 w-20 rounded" />
    </div>
  )
}

export default function DashboardPage() {
  const [logs, setLogs] = useState<UsageLog[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [hourlyData, setHourlyData] = useState<{ hour: string; cost: number }[]>([])
  const [dailyData, setDailyData] = useState<{ date: string; cost: number }[]>([])

  useEffect(() => {
    getMyUsage({ page: 1, page_size: 5 })
      .then((res) => {
        setLogs(res.data.logs || [])
        setTotal(res.data.total || 0)
      })
      .finally(() => setLoading(false))

    const today = toDateStr(new Date())
    getMyUsage({ page: 1, page_size: 1000, start_date: today, end_date: today })
      .then((res) => {
        const todayLogs: UsageLog[] = res.data.logs || []
        const buckets: Record<string, number> = {}
        for (let h = 0; h < 24; h++) {
          buckets[String(h).padStart(2, '0')] = 0
        }
        todayLogs.forEach((l) => {
          const h = new Date(l.created_at).getHours()
          buckets[String(h).padStart(2, '0')] += l.cost_usd
        })
        setHourlyData(
          Object.entries(buckets).map(([hour, cost]) => ({ hour: hour + ':00', cost: parseFloat(cost.toFixed(6)) }))
        )
      })

    const end = today
    const start = toDateStr(new Date(new Date(today).getTime() - 13 * 86400000))
    getMyDailyStats({ start_date: start, end_date: end })
      .then((res) => {
        const stats: DailyStat[] = res.data.stats || []
        const map: Record<string, number> = {}
        stats.forEach((s) => { map[s.date] = (map[s.date] || 0) + s.cost_usd })
        const result = []
        for (let i = 13; i >= 0; i--) {
          const d = toDateStr(new Date(new Date(today).getTime() - i * 86400000))
          result.push({ date: d.slice(5), cost: parseFloat((map[d] || 0).toFixed(6)) })
        }
        setDailyData(result)
      })
  }, [])

  const totalTokens = logs.reduce((s, l) => s + l.total_tokens, 0)
  const totalCost = logs.reduce((s, l) => s + l.cost_usd, 0)

  return (
    <div className="p-8">
      {/* Page header */}
      <div className="mb-7">
        <h2 className="text-xl font-bold text-gray-900">仪表盘</h2>
        <p className="text-sm text-gray-400 mt-0.5">查看你的 API 使用概览</p>
      </div>

      {/* Stat cards */}
      <div className="grid grid-cols-3 gap-4 mb-7">
        {loading ? (
          <>
            <StatCardSkeleton />
            <StatCardSkeleton />
            <StatCardSkeleton />
          </>
        ) : (
          <>
            <StatCard label="总请求数" value={total} accent="blue" />
            <StatCard label="Token 消耗（近5条）" value={totalTokens.toLocaleString()} accent="purple" />
            <StatCard label="费用（近5条）" value={`$${totalCost.toFixed(4)}`} accent="red" />
          </>
        )}
      </div>

      {/* Charts */}
      <div className="grid grid-cols-2 gap-4 mb-7">
        <div className="bg-white rounded-xl border border-gray-100 p-5 shadow-sm">
          <h3 className="text-sm font-semibold text-gray-700 mb-4">今日费用（按小时）</h3>
          <ResponsiveContainer width="100%" height={180}>
            <BarChart data={hourlyData}>
              <CartesianGrid strokeDasharray="3 3" stroke="#f0f0f0" />
              <XAxis dataKey="hour" tick={{ fontSize: 9 }} interval={3} />
              <YAxis tick={{ fontSize: 10 }} tickFormatter={(v) => `$${v}`} />
              <Tooltip
                contentStyle={{ borderRadius: 8, border: '1px solid #e5e7eb', fontSize: 12 }}
                formatter={(v: number) => [`$${v.toFixed(6)}`, '费用']}
              />
              <Bar dataKey="cost" fill="#DC2626" radius={[3, 3, 0, 0]} />
            </BarChart>
          </ResponsiveContainer>
        </div>
        <div className="bg-white rounded-xl border border-gray-100 p-5 shadow-sm">
          <h3 className="text-sm font-semibold text-gray-700 mb-4">近14天费用</h3>
          <ResponsiveContainer width="100%" height={180}>
            <BarChart data={dailyData}>
              <CartesianGrid strokeDasharray="3 3" stroke="#f0f0f0" />
              <XAxis dataKey="date" tick={{ fontSize: 10 }} />
              <YAxis tick={{ fontSize: 10 }} tickFormatter={(v) => `$${v}`} />
              <Tooltip
                contentStyle={{ borderRadius: 8, border: '1px solid #e5e7eb', fontSize: 12 }}
                formatter={(v: number) => [`$${v.toFixed(6)}`, '费用']}
              />
              <Bar dataKey="cost" fill="#DC2626" radius={[3, 3, 0, 0]} />
            </BarChart>
          </ResponsiveContainer>
        </div>
      </div>

      {/* Recent requests table */}
      <div className="bg-white rounded-xl border border-gray-100 shadow-sm overflow-hidden">
        <div className="px-6 py-4 border-b border-gray-100 flex items-center justify-between">
          <h3 className="text-sm font-semibold text-gray-700">最近请求</h3>
          <span className="text-xs text-gray-400">近 5 条</span>
        </div>
        <table className="w-full text-sm">
          <thead className="bg-gray-50/80">
            <tr>
              {['模型', '输入', '输出', '费用', '状态', '时间'].map((h) => (
                <th key={h} className="px-4 py-3 text-left text-xs font-semibold text-gray-400 uppercase tracking-wide">
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-50">
            {loading ? (
              Array.from({ length: 3 }).map((_, i) => <SkeletonRow key={i} />)
            ) : logs.length === 0 ? (
              <tr>
                <td colSpan={6} className="px-4 py-10 text-center text-sm text-gray-400">暂无数据</td>
              </tr>
            ) : (
              logs.map((log) => (
                <tr key={log.id} className="hover:bg-gray-50/50 transition-colors">
                  <td className="px-4 py-3 font-mono text-xs text-gray-600">{log.model}</td>
                  <td className="px-4 py-3 text-gray-600">{log.input_tokens.toLocaleString()}</td>
                  <td className="px-4 py-3 text-gray-600">{log.output_tokens.toLocaleString()}</td>
                  <td className="px-4 py-3 font-medium text-gray-800">${log.cost_usd.toFixed(4)}</td>
                  <td className="px-4 py-3">
                    <span
                      className={`inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium ${
                        log.status_code === 200
                          ? 'bg-green-50 text-green-700 ring-1 ring-green-100'
                          : 'bg-red-50 text-red-700 ring-1 ring-red-100'
                      }`}
                    >
                      {log.status_code}
                    </span>
                  </td>
                  <td className="px-4 py-3 text-gray-400 text-xs">
                    {new Date(log.created_at).toLocaleString()}
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

function StatCard({ label, value, accent }: { label: string; value: string | number; accent: 'red' | 'blue' | 'purple' }) {
  const colors = {
    red: 'bg-red-50 text-red-600',
    blue: 'bg-blue-50 text-blue-600',
    purple: 'bg-purple-50 text-purple-600',
  }
  return (
    <div className="bg-white rounded-xl border border-gray-100 px-6 py-5 shadow-sm hover:shadow-md transition-shadow">
      <div className={`inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium mb-3 ${colors[accent]}`}>
        {label}
      </div>
      <p className="text-2xl font-bold text-gray-900">{value}</p>
    </div>
  )
}
