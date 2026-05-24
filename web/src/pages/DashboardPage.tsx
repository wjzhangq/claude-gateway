import { useEffect, useState } from 'react'
import { getMyUsage, getMyDailyStats, getMyDashboard } from '../api'
import { formatTime, toDateStr } from '../utils/time'
import {
  BarChart, Bar, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer,
} from 'recharts'

interface UsageLog {
  id: number
  model: string
  provider: string
  input_tokens: number
  output_tokens: number
  total_tokens: number
  cost_usd: number
  status_code: number
  is_openclaw: boolean
  ua: string
  created_at: string
}

interface DailyStat {
  date: string
  cost_usd: number
}

interface ModelUsage {
  provider: string
  model: string
  cost: number
  requests: number
  input_tokens: number
  output_tokens: number
}

const providerColors: Record<string, string> = {
  backend: 'bg-blue-50 text-blue-700 ring-blue-100',
  aws: 'bg-green-50 text-green-700 ring-green-100',
  kimi: 'bg-purple-50 text-purple-700 ring-purple-100',
  minimax: 'bg-orange-50 text-orange-700 ring-orange-100',
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
  const [loading, setLoading] = useState(true)
  const [hourlyData, setHourlyData] = useState<{ hour: string; cost: number }[]>([])
  const [dailyData, setDailyData] = useState<{ date: string; cost: number }[]>([])
  const [todayCost, setTodayCost] = useState(0)
  const [modelUsage, setModelUsage] = useState<ModelUsage[]>([])
  const [quota, setQuota] = useState<{
    backend_daily_limit: number
    backend_daily_used: number
    backend_daily_remaining: number
    aws_daily_limit: number
    aws_daily_used: number
    aws_daily_remaining: number
  } | null>(null)

  useEffect(() => {
    getMyDashboard()
      .then((res) => setQuota(res.data))
      .catch(() => {})

    const today = toDateStr(new Date())
    getMyUsage({ page: 1, page_size: 2000, start_date: today, end_date: today })
      .then((res) => {
        const todayLogs: UsageLog[] = res.data.logs || []
        const buckets: Record<string, number> = {}
        for (let h = 0; h < 24; h++) {
          buckets[String(h).padStart(2, '0')] = 0
        }
        let costSum = 0
        const modelMap: Record<string, ModelUsage> = {}

        todayLogs.forEach((l) => {
          const h = new Date(l.created_at).getHours()
          buckets[String(h).padStart(2, '0')] += l.cost_usd
          costSum += l.cost_usd

          const key = `${l.provider}|${l.model}`
          if (!modelMap[key]) {
            modelMap[key] = { provider: l.provider, model: l.model, cost: 0, requests: 0, input_tokens: 0, output_tokens: 0 }
          }
          modelMap[key].cost += l.cost_usd
          modelMap[key].requests++
          modelMap[key].input_tokens += l.input_tokens
          modelMap[key].output_tokens += l.output_tokens
        })

        setTodayCost(costSum)
        setHourlyData(
          Object.entries(buckets).map(([hour, cost]) => ({ hour: hour + ':00', cost: parseFloat(cost.toFixed(6)) }))
        )
        setModelUsage(Object.values(modelMap).sort((a, b) => b.cost - a.cost))
      })
      .finally(() => setLoading(false))

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

  return (
    <div className="p-8">
      <div className="mb-7">
        <h2 className="text-xl font-bold text-gray-900">仪表盘</h2>
        <p className="text-sm text-gray-400 mt-0.5">查看你的 API 使用概览</p>
      </div>

      {/* Quota cards */}
      {quota && (
        <div className="grid grid-cols-2 gap-4 mb-6">
          {quota.backend_daily_limit > 0 && (
            <div className="bg-white rounded-xl border border-gray-100 px-6 py-5 shadow-sm">
              <div className="flex items-center justify-between mb-3">
                <span className="text-xs font-semibold text-gray-500 uppercase tracking-wide">Backend 每日限额</span>
                <span className="text-xs text-gray-400">
                  ${quota.backend_daily_used.toFixed(2)} / ${quota.backend_daily_limit.toFixed(2)}
                </span>
              </div>
              <div className="w-full bg-gray-100 rounded-full h-2.5 mb-2">
                <div
                  className={`h-2.5 rounded-full transition-all ${
                    quota.backend_daily_remaining <= 0 ? 'bg-red-500'
                    : quota.backend_daily_used / quota.backend_daily_limit > 0.8 ? 'bg-amber-500'
                    : 'bg-green-500'
                  }`}
                  style={{ width: `${Math.min((quota.backend_daily_used / quota.backend_daily_limit) * 100, 100)}%` }}
                />
              </div>
              <span className="text-lg font-bold text-gray-900">
                ${quota.backend_daily_remaining.toFixed(2)}
                <span className="text-xs font-normal text-gray-400 ml-1">剩余</span>
              </span>
            </div>
          )}
          {quota.aws_daily_limit > 0 && (
            <div className="bg-white rounded-xl border border-gray-100 px-6 py-5 shadow-sm">
              <div className="flex items-center justify-between mb-3">
                <span className="text-xs font-semibold text-gray-500 uppercase tracking-wide">AWS 每日限额</span>
                <span className="text-xs text-gray-400">
                  ${quota.aws_daily_used.toFixed(2)} / ${quota.aws_daily_limit.toFixed(2)}
                </span>
              </div>
              <div className="w-full bg-gray-100 rounded-full h-2.5 mb-2">
                <div
                  className={`h-2.5 rounded-full transition-all ${
                    quota.aws_daily_remaining <= 0 ? 'bg-red-500'
                    : quota.aws_daily_used / quota.aws_daily_limit > 0.8 ? 'bg-amber-500'
                    : 'bg-green-500'
                  }`}
                  style={{ width: `${Math.min((quota.aws_daily_used / quota.aws_daily_limit) * 100, 100)}%` }}
                />
              </div>
              <span className="text-lg font-bold text-gray-900">
                ${quota.aws_daily_remaining.toFixed(2)}
                <span className="text-xs font-normal text-gray-400 ml-1">剩余</span>
              </span>
            </div>
          )}
        </div>
      )}

      {/* Today cost card */}
      <div className="grid grid-cols-3 gap-4 mb-7">
        {loading ? (
          <><StatCardSkeleton /><StatCardSkeleton /><StatCardSkeleton /></>
        ) : (
          <>
            <StatCard label="今日总费用" value={`$${todayCost.toFixed(4)}`} accent="red" />
            <StatCard label="今日请求数" value={modelUsage.reduce((s, m) => s + m.requests, 0).toLocaleString()} accent="blue" />
            <StatCard label="使用模型数" value={modelUsage.length} accent="purple" />
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
                formatter={(v) => [`$${Number(v).toFixed(6)}`, '费用']}
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
                formatter={(v) => [`$${Number(v).toFixed(6)}`, '费用']}
              />
              <Bar dataKey="cost" fill="#DC2626" radius={[3, 3, 0, 0]} />
            </BarChart>
          </ResponsiveContainer>
        </div>
      </div>

      {/* Model usage breakdown */}
      {modelUsage.length > 0 && (
        <div className="bg-white rounded-xl border border-gray-100 shadow-sm overflow-hidden">
          <div className="px-6 py-4 border-b border-gray-100">
            <h3 className="text-sm font-semibold text-gray-700">今日按模型使用量</h3>
          </div>
          <table className="w-full text-sm">
            <thead className="bg-gray-50/80">
              <tr>
                {['渠道', '模型', '费用', '请求数', '输入', '输出'].map((h) => (
                  <th key={h} className="px-4 py-3 text-left text-xs font-semibold text-gray-400 uppercase tracking-wide">{h}</th>
                ))}
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-50">
              {modelUsage.map((m, i) => (
                <tr key={i} className="hover:bg-gray-50/50">
                  <td className="px-4 py-3">
                    <span className={`inline-flex items-center px-1.5 py-0.5 rounded text-[10px] font-semibold ring-1 ${providerColors[m.provider] || 'bg-gray-50 text-gray-700 ring-gray-100'}`}>
                      {m.provider}
                    </span>
                  </td>
                  <td className="px-4 py-3 font-mono text-xs text-gray-700">{m.model}</td>
                  <td className="px-4 py-3 font-medium text-gray-800">${m.cost.toFixed(4)}</td>
                  <td className="px-4 py-3 text-gray-600">{m.requests}</td>
                  <td className="px-4 py-3 text-gray-600">{(m.input_tokens / 1000).toFixed(1)}K</td>
                  <td className="px-4 py-3 text-gray-600">{(m.output_tokens / 1000).toFixed(1)}K</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
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
