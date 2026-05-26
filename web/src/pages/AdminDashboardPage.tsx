import { useEffect, useState } from 'react'
import { adminGetOverview, adminGetProviderDailyStats } from '../api'
import { toDateStr } from '../utils/time'
import {
  BarChart, Bar, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer,
} from 'recharts'

interface Overview {
  total_users: number
  active_users: number
  total_keys: number
  today_requests: number
  today_cost: number
  today_oc_cost: number
  month_requests: number
  month_cost: number
}

interface DailyModelStat {
  date: string
  model: string
  requests: number
  cost_usd: number
}

export default function AdminDashboardPage() {
  const [overview, setOverview] = useState<Overview | null>(null)
  const [loading, setLoading] = useState(true)
  const [chartData, setChartData] = useState<{ date: string; cost: number }[]>([])

  useEffect(() => {
    setLoading(true)
    const endDate = toDateStr(new Date())
    const startDate = toDateStr(new Date(Date.now() - 13 * 86400000))

    Promise.all([
      adminGetOverview(),
      adminGetProviderDailyStats('backend', startDate, endDate),
    ])
      .then(([overviewRes, dailyRes]) => {
        setOverview(overviewRes.data)
        const stats: DailyModelStat[] = dailyRes.data.stats || []
        const grouped = stats.reduce((acc, s) => {
          const existing = acc.find(d => d.date === s.date)
          if (existing) { existing.cost += s.cost_usd }
          else acc.push({ date: s.date, cost: s.cost_usd })
          return acc
        }, [] as { date: string; cost: number }[])
        setChartData(
          grouped
            .sort((a, b) => a.date.localeCompare(b.date))
            .map(d => ({ date: d.date.slice(5), cost: +d.cost.toFixed(4) }))
        )
      })
      .catch(() => {})
      .finally(() => setLoading(false))
  }, [])

  const ocRatio = overview && overview.today_cost > 0
    ? ((overview.today_oc_cost / overview.today_cost) * 100).toFixed(1)
    : '0.0'

  return (
    <div className="p-8">
      <div className="mb-7">
        <h2 className="text-xl font-bold text-gray-900">管理总览</h2>
        <p className="text-sm text-gray-400 mt-0.5">系统整体运行情况</p>
      </div>

      <div className="grid grid-cols-4 gap-4 mb-6">
        <StatCard label="总用户数" value={loading ? '...' : (overview?.total_users ?? 0).toLocaleString()} />
        <StatCard label="活跃用户" value={loading ? '...' : (overview?.active_users ?? 0).toLocaleString()} />
        <StatCard label="活跃 Key" value={loading ? '...' : (overview?.total_keys ?? 0).toLocaleString()} />
        <StatCard label="龙虾占比" value={loading ? '...' : `${ocRatio}%`} highlight />
      </div>

      <div className="grid grid-cols-4 gap-4 mb-6">
        <StatCard label="今日请求" value={loading ? '...' : (overview?.today_requests ?? 0).toLocaleString()} />
        <StatCard label="今日费用" value={loading ? '...' : `$${(overview?.today_cost ?? 0).toFixed(4)}`} />
        <StatCard label="本月请求" value={loading ? '...' : (overview?.month_requests ?? 0).toLocaleString()} />
        <StatCard label="本月费用" value={loading ? '...' : `$${(overview?.month_cost ?? 0).toFixed(4)}`} />
      </div>

      {chartData.length > 0 && (
        <div className="bg-white rounded-xl border border-gray-100 p-5 shadow-sm">
          <h3 className="text-sm font-semibold text-gray-700 mb-4">近14天 Backend 费用趋势 (USD)</h3>
          <ResponsiveContainer width="100%" height={220}>
            <BarChart data={chartData}>
              <CartesianGrid strokeDasharray="3 3" stroke="#f0f0f0" />
              <XAxis dataKey="date" tick={{ fontSize: 10 }} />
              <YAxis tick={{ fontSize: 11 }} tickFormatter={(v) => `$${v}`} />
              <Tooltip
                contentStyle={{ borderRadius: 8, border: '1px solid #e5e7eb', fontSize: 12 }}
                formatter={((value: number) => [`$${value.toFixed(4)}`, '费用']) as any}
              />
              <Bar dataKey="cost" name="费用" fill="#DC2626" radius={[3, 3, 0, 0]} />
            </BarChart>
          </ResponsiveContainer>
        </div>
      )}
    </div>
  )
}

function StatCard({ label, value, highlight }: { label: string; value: string | number; highlight?: boolean }) {
  return (
    <div className="bg-white rounded-xl border border-gray-100 px-6 py-5 shadow-sm">
      <div className="text-xs font-semibold text-gray-400 uppercase tracking-wide mb-2">{label}</div>
      <p className={`text-2xl font-bold ${highlight ? 'text-orange-600' : 'text-gray-900'}`}>{value}</p>
    </div>
  )
}
