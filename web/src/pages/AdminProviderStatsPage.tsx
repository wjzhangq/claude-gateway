import { useEffect, useState } from 'react'
import { adminGetProviderStats, adminGetProviderModelStats, adminGetBackendStatus, adminGetProviderDailyStats } from '../api'
import { toDateStr } from '../utils/time'
import {
  BarChart, Bar, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer,
} from 'recharts'

interface ModelStat {
  model: string
  requests: number
  input_tokens: number
  output_tokens: number
  total_tokens: number
  cost_usd: number
}

interface DailyModelStat {
  date: string
  model: string
  requests: number
  input_tokens: number
  output_tokens: number
  total_tokens: number
  cost_usd: number
}

interface BackendStatus {
  name: string
  weight: number
  is_healthy: boolean
  total_requests: number
  error_count: number
}

const providerTitles: Record<string, string> = {
  backend: 'Backend (网梯)',
  aws: 'AWS Bedrock',
  kimi: 'Kimi',
  minimax: 'MiniMax',
}

export default function AdminProviderStatsPage({ provider }: { provider: string }) {
  const [todayRequests, setTodayRequests] = useState(0)
  const [todayCost, setTodayCost] = useState(0)
  const [monthRequests, setMonthRequests] = useState(0)
  const [monthCost, setMonthCost] = useState(0)
  const [modelStats, setModelStats] = useState<ModelStat[]>([])
  const [backends, setBackends] = useState<BackendStatus[]>([])
  const [loading, setLoading] = useState(true)

  const [startDate, setStartDate] = useState(() => {
    const d = new Date(); d.setDate(d.getDate() - 13)
    return toDateStr(d)
  })
  const [endDate, setEndDate] = useState(() => toDateStr(new Date()))
  const [dailyModelStats, setDailyModelStats] = useState<DailyModelStat[]>([])
  const [chartLoading, setChartLoading] = useState(true)

  useEffect(() => {
    setLoading(true)
    const today = new Date().toISOString().slice(0, 10)

    Promise.all([
      adminGetProviderStats(provider, 'today'),
      adminGetProviderStats(provider, 'month'),
      adminGetProviderModelStats(provider, today),
      provider === 'backend' ? adminGetBackendStatus() : Promise.resolve(null),
    ])
      .then(([todayRes, monthRes, modelRes, backendRes]) => {
        setTodayRequests(todayRes.data.requests || 0)
        setTodayCost(todayRes.data.cost_usd || 0)
        setMonthRequests(monthRes.data.requests || 0)
        setMonthCost(monthRes.data.cost_usd || 0)
        setModelStats(modelRes.data.stats || [])
        if (backendRes?.data) {
          setBackends(backendRes.data || [])
        }
      })
      .catch(() => {})
      .finally(() => setLoading(false))
  }, [provider])

  useEffect(() => {
    setChartLoading(true)
    adminGetProviderDailyStats(provider, startDate, endDate)
      .then((res) => setDailyModelStats(res.data.stats || []))
      .catch(() => setDailyModelStats([]))
      .finally(() => setChartLoading(false))
  }, [provider, startDate, endDate])

  const chartData = dailyModelStats
    .reduce((acc, s) => {
      const existing = acc.find(d => d.date === s.date)
      if (existing) { existing.cost += s.cost_usd; existing.requests += s.requests }
      else acc.push({ date: s.date, cost: s.cost_usd, requests: s.requests })
      return acc
    }, [] as { date: string; cost: number; requests: number }[])
    .sort((a, b) => a.date.localeCompare(b.date))
    .map(d => ({ ...d, date: d.date.slice(5), cost: +d.cost.toFixed(4) }))

  const title = providerTitles[provider] || provider

  return (
    <div className="p-8">
      <div className="mb-7">
        <h2 className="text-xl font-bold text-gray-900">{title} 统计</h2>
        <p className="text-sm text-gray-400 mt-0.5">查看 {title} 渠道的使用情况</p>
      </div>

      {/* Today stats */}
      <div className="mb-6">
        <p className="text-xs font-semibold text-gray-400 uppercase tracking-widest mb-3">当日</p>
        <div className="grid grid-cols-3 gap-4">
          <StatCard label="请求数" value={loading ? '...' : todayRequests.toLocaleString()} />
          <StatCard label="费用" value={loading ? '...' : `$${todayCost.toFixed(4)}`} />
          <StatCard label="Token 总量" value={loading ? '...' : modelStats.reduce((s, m) => s + m.total_tokens, 0).toLocaleString()} />
        </div>
      </div>

      {/* Model breakdown */}
      {modelStats.length > 0 && (
        <div className="mb-6 bg-white rounded-xl border border-gray-100 shadow-sm overflow-hidden">
          <div className="px-6 py-4 border-b border-gray-100">
            <h3 className="text-sm font-semibold text-gray-700">当日按模型统计</h3>
          </div>
          <table className="w-full text-sm">
            <thead className="bg-gray-50/80">
              <tr>
                {['模型', '请求数', '输入 Token', '输出 Token', '费用'].map((h) => (
                  <th key={h} className="px-4 py-3 text-left text-xs font-semibold text-gray-400 uppercase tracking-wide">{h}</th>
                ))}
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-50">
              {modelStats.map((m) => (
                <tr key={m.model} className="hover:bg-gray-50/50">
                  <td className="px-4 py-3 font-mono text-xs text-gray-700">{m.model}</td>
                  <td className="px-4 py-3 text-gray-600">{m.requests.toLocaleString()}</td>
                  <td className="px-4 py-3 text-gray-600">{m.input_tokens.toLocaleString()}</td>
                  <td className="px-4 py-3 text-gray-600">{m.output_tokens.toLocaleString()}</td>
                  <td className="px-4 py-3 font-medium text-gray-800">${m.cost_usd.toFixed(4)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* Month stats */}
      <div className="mb-6">
        <p className="text-xs font-semibold text-gray-400 uppercase tracking-widest mb-3">当月</p>
        <div className="grid grid-cols-2 gap-4">
          <StatCard label="请求数" value={loading ? '...' : monthRequests.toLocaleString()} />
          <StatCard label="费用" value={loading ? '...' : `$${monthCost.toFixed(4)}`} />
        </div>
      </div>

      {/* Date range trend chart */}
      <div className="mb-6">
        <div className="flex items-center gap-3 mb-4">
          <p className="text-xs font-semibold text-gray-400 uppercase tracking-widest">费用趋势</p>
          <input
            type="date"
            value={startDate}
            onChange={(e) => setStartDate(e.target.value)}
            className="px-3 py-1.5 border border-gray-200 rounded-lg text-sm bg-white focus:outline-none focus:ring-2 focus:ring-red-500/30 focus:border-red-400"
          />
          <span className="text-gray-400">—</span>
          <input
            type="date"
            value={endDate}
            onChange={(e) => setEndDate(e.target.value)}
            className="px-3 py-1.5 border border-gray-200 rounded-lg text-sm bg-white focus:outline-none focus:ring-2 focus:ring-red-500/30 focus:border-red-400"
          />
        </div>

        {chartLoading ? (
          <div className="bg-white rounded-xl border border-gray-100 p-5 shadow-sm text-center text-gray-400 text-sm">加载中...</div>
        ) : chartData.length > 0 ? (
          <div className="bg-white rounded-xl border border-gray-100 p-5 shadow-sm">
            <ResponsiveContainer width="100%" height={200}>
              <BarChart data={chartData}>
                <CartesianGrid strokeDasharray="3 3" stroke="#f0f0f0" />
                <XAxis dataKey="date" tick={{ fontSize: 10 }} />
                <YAxis tick={{ fontSize: 11 }} tickFormatter={(v) => `$${v}`} />
                <Tooltip contentStyle={{ borderRadius: 8, border: '1px solid #e5e7eb', fontSize: 12 }} formatter={((value: number) => [`$${value.toFixed(4)}`, '费用']) as any} />
                <Bar dataKey="cost" name="费用" fill="#DC2626" radius={[3, 3, 0, 0]} />
              </BarChart>
            </ResponsiveContainer>
          </div>
        ) : (
          <div className="bg-white rounded-xl border border-gray-100 p-5 shadow-sm text-center text-gray-400 text-sm">暂无数据</div>
        )}
      </div>

      {/* Daily model breakdown table */}
      {dailyModelStats.length > 0 && (
        <div className="mb-6 bg-white rounded-xl border border-gray-100 shadow-sm overflow-hidden">
          <div className="px-6 py-4 border-b border-gray-100">
            <h3 className="text-sm font-semibold text-gray-700">按日期/模型明细</h3>
          </div>
          <table className="w-full text-sm">
            <thead className="bg-gray-50/80">
              <tr>
                {['日期', '模型', '请求数', '输入 Token', '输出 Token', '费用'].map((h) => (
                  <th key={h} className="px-4 py-3 text-left text-xs font-semibold text-gray-400 uppercase tracking-wide">{h}</th>
                ))}
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-50">
              {dailyModelStats.map((s) => (
                <tr key={`${s.date}-${s.model}`} className="hover:bg-gray-50/50">
                  <td className="px-4 py-3 text-gray-600">{s.date}</td>
                  <td className="px-4 py-3 font-mono text-xs text-gray-700">{s.model}</td>
                  <td className="px-4 py-3 text-gray-600">{s.requests.toLocaleString()}</td>
                  <td className="px-4 py-3 text-gray-600">{s.input_tokens.toLocaleString()}</td>
                  <td className="px-4 py-3 text-gray-600">{s.output_tokens.toLocaleString()}</td>
                  <td className="px-4 py-3 font-medium text-gray-800">${s.cost_usd.toFixed(4)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* Backend accounts (only for provider=backend) */}
      {provider === 'backend' && backends.length > 0 && (
        <div className="bg-white rounded-xl border border-gray-100 shadow-sm overflow-hidden">
          <div className="px-6 py-4 border-b border-gray-100">
            <h3 className="text-sm font-semibold text-gray-700">后端账号状态</h3>
          </div>
          <table className="w-full text-sm">
            <thead className="bg-gray-50/80">
              <tr>
                {['名称', '状态', '权重', '请求数', '错误数'].map((h) => (
                  <th key={h} className="px-4 py-3 text-left text-xs font-semibold text-gray-400 uppercase tracking-wide">{h}</th>
                ))}
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-50">
              {backends.map((b) => (
                <tr key={b.name} className="hover:bg-gray-50/50">
                  <td className="px-4 py-3 font-medium text-gray-700">{b.name}</td>
                  <td className="px-4 py-3">
                    <span className={`inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium ring-1 ${
                      b.is_healthy
                        ? 'bg-green-50 text-green-700 ring-green-100'
                        : 'bg-red-50 text-red-700 ring-red-100'
                    }`}>
                      {b.is_healthy ? '正常' : '异常'}
                    </span>
                  </td>
                  <td className="px-4 py-3 text-gray-600">{b.weight}</td>
                  <td className="px-4 py-3 text-gray-600">{b.total_requests?.toLocaleString() || 0}</td>
                  <td className="px-4 py-3 text-gray-600">{b.error_count || 0}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

function StatCard({ label, value }: { label: string; value: string | number }) {
  return (
    <div className="bg-white rounded-xl border border-gray-100 px-6 py-5 shadow-sm">
      <div className="text-xs font-semibold text-gray-400 uppercase tracking-wide mb-2">{label}</div>
      <p className="text-2xl font-bold text-gray-900">{value}</p>
    </div>
  )
}
