import { useState, useEffect } from 'react'
import {
  LineChart, Line, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer, Legend,
} from 'recharts'
import { adminGetUserInsight, adminGetInsightRanking, type InsightRankingUser } from '../api'

function formatTokens(n: number): string {
  if (!n) return '0'
  if (n >= 1_000_000_000) return (n / 1_000_000_000).toFixed(2) + 'B'
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(0) + 'K'
  return n.toString()
}

interface ModelStat { model: string; requests: number; tokens: number; cost: number }
interface TrendPoint { date: string; requests: number; tokens: number; cost: number }
interface Summary {
  requests: number; tokens: number; input_tokens: number; output_tokens: number
  cost: number; first_date: string; last_date: string; active_days: number
}
interface Insight {
  user: { id: number; itcode: string; name: string; status: string; aws_enabled: boolean }
  org_tag: { department: string; role_tag: string; note: string }
  summary: { backend: Summary | null; aws: Summary | null }
  model_distribution: { backend: ModelStat[]; aws: ModelStat[] }
  daily_trend: { backend: TrendPoint[]; aws: TrendPoint[] }
  weekly_trend: { week: string; tokens: number; cost: number }[]
  peak_days: { date: string; tokens: number; cost: number }[]
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <div className="text-xs text-gray-400">{label}</div>
      <div className="font-semibold text-gray-800">{value}</div>
    </div>
  )
}

function SummaryCard({ channel, s }: { channel: string; s: Summary }) {
  return (
    <div className="bg-white rounded-xl shadow-sm p-5">
      <h4 className="mb-3 flex items-center gap-2">
        <span className={`px-1.5 py-0.5 rounded text-[10px] font-medium uppercase ${
          channel === 'aws' ? 'bg-orange-100 text-orange-700' : 'bg-blue-100 text-blue-700'
        }`}>{channel}</span>
        <span className="text-sm font-medium text-gray-700">渠道汇总</span>
      </h4>
      <div className="grid grid-cols-2 gap-3 text-sm">
        <Stat label="总 Tokens" value={formatTokens(s.tokens)} />
        <Stat label="请求次数" value={s.requests.toLocaleString()} />
        <Stat label="Input" value={formatTokens(s.input_tokens)} />
        <Stat label="Output" value={formatTokens(s.output_tokens)} />
        <Stat label="费用估算" value={'$' + s.cost.toLocaleString()} />
        <Stat label="活跃天数" value={s.active_days + ' 天'} />
      </div>
    </div>
  )
}

export default function AdminInsightUserPage(
  { userId, onSelectUser }: { userId?: number; onSelectUser?: (userId: number | null) => void } = {}
) {
  const id = userId ? String(userId) : ''
  const [users, setUsers] = useState<InsightRankingUser[]>([])
  const [insight, setInsight] = useState<Insight | null>(null)
  const [loading, setLoading] = useState(false)

  // Load user list for the picker dropdown
  useEffect(() => {
    adminGetInsightRanking({ limit: 500 })
      .then(({ data }) => setUsers(data.ranking || []))
      .catch(() => {})
  }, [])

  useEffect(() => {
    if (!id) {
      setInsight(null)
      return
    }
    setLoading(true)
    adminGetUserInsight(Number(id))
      .then(({ data }) => setInsight(data))
      .finally(() => setLoading(false))
  }, [id])

  const picker = (
    <div className="flex items-center gap-3 mb-4">
      <select
        value={id || ''}
        onChange={e => onSelectUser?.(e.target.value ? Number(e.target.value) : null)}
        className="px-3 py-1.5 border border-gray-200 rounded-lg text-sm w-72 focus:outline-none focus:border-red-400"
      >
        <option value="">选择用户查看洞察报告…</option>
        {users.map(u => (
          <option key={u.user_id} value={u.user_id}>
            {u.name || u.itcode} — {formatTokens(u.all_tokens)} tokens
          </option>
        ))}
      </select>
    </div>
  )

  if (loading) {
    return (
      <div className="p-6 max-w-[1200px] mx-auto">
        {picker}
        <div className="p-10 text-center text-gray-400">加载中…</div>
      </div>
    )
  }

  if (!insight) {
    return (
      <div className="p-6 max-w-[1200px] mx-auto">
        <h1 className="text-xl font-semibold text-gray-900 mb-4">用户洞察</h1>
        {picker}
        <div className="p-10 text-center text-gray-400">请选择一个用户查看洞察报告</div>
      </div>
    )
  }

  // Merge backend + aws daily trend by date for the chart
  const trendMap: Record<string, { date: string; backend: number; aws: number }> = {}
  for (const p of insight.daily_trend.backend || []) {
    trendMap[p.date] = { date: p.date, backend: p.tokens, aws: 0 }
  }
  for (const p of insight.daily_trend.aws || []) {
    if (!trendMap[p.date]) trendMap[p.date] = { date: p.date, backend: 0, aws: 0 }
    trendMap[p.date].aws = p.tokens
  }
  const trend = Object.values(trendMap).sort((a, b) => a.date.localeCompare(b.date))

  return (
    <div className="p-6 max-w-[1200px] mx-auto">
      <h1 className="text-xl font-semibold text-gray-900 mb-4">用户洞察</h1>
      {picker}
      <h2 className="text-lg font-semibold text-gray-900 mb-4">{insight.user.name || insight.user.itcode} · 数据洞察</h2>

      {/* Base info */}
      <div className="bg-white rounded-xl shadow-sm p-5 mb-4 grid grid-cols-4 gap-3">
        <Stat label="ITCode" value={insight.user.itcode} />
        <Stat label="状态" value={insight.user.status} />
        <Stat label="部门" value={insight.org_tag.department || '未设置'} />
        <Stat label="角色 / AWS" value={`${insight.org_tag.role_tag} · ${insight.user.aws_enabled ? '已开通' : '未开通'}`} />
      </div>

      {/* Summary */}
      <div className="grid grid-cols-2 gap-4 mb-4">
        {insight.summary.backend && <SummaryCard channel="backend" s={insight.summary.backend} />}
        {insight.summary.aws && <SummaryCard channel="aws" s={insight.summary.aws} />}
      </div>

      {/* Daily trend chart */}
      {trend.length > 0 && (
        <div className="bg-white rounded-xl shadow-sm p-5 mb-4">
          <h4 className="text-sm font-medium text-gray-700 mb-3">近 30 天 Token 趋势</h4>
          <ResponsiveContainer width="100%" height={260}>
            <LineChart data={trend} margin={{ top: 5, right: 20, bottom: 5, left: 0 }}>
              <CartesianGrid strokeDasharray="3 3" stroke="#f0f0f0" />
              <XAxis dataKey="date" tick={{ fontSize: 11 }} tickFormatter={(d: string) => d.slice(5)} />
              <YAxis tick={{ fontSize: 11 }} tickFormatter={formatTokens} />
              <Tooltip formatter={(v) => formatTokens(Number(v))} />
              <Legend />
              <Line type="monotone" dataKey="backend" name="Backend" stroke="#dc2626" strokeWidth={2} dot={false} />
              <Line type="monotone" dataKey="aws" name="AWS" stroke="#ea580c" strokeWidth={2} dot={false} />
            </LineChart>
          </ResponsiveContainer>
        </div>
      )}

      {/* Model distribution */}
      <div className="bg-white rounded-xl shadow-sm p-5 mb-4">
        <h4 className="text-sm font-medium text-gray-700 mb-3">模型使用分布</h4>
        {(['backend', 'aws'] as const).map(ch => {
          const models = insight.model_distribution[ch] || []
          if (models.length === 0) return null
          return (
            <div key={ch} className="mb-3">
              <h5 className="text-xs text-gray-400 mb-2 uppercase">{ch}</h5>
              <div className="flex flex-wrap gap-2">
                {models.slice(0, 12).map(m => (
                  <div key={m.model} className="bg-gray-50 rounded-lg px-3 py-2">
                    <div className="text-xs font-medium text-gray-700">{m.model}</div>
                    <div className="text-[11px] text-gray-400">{formatTokens(m.tokens)} · {m.requests} 次 · ${m.cost}</div>
                  </div>
                ))}
              </div>
            </div>
          )
        })}
      </div>

      {/* Peak days */}
      {insight.peak_days.length > 0 && (
        <div className="bg-white rounded-xl shadow-sm p-5">
          <h4 className="text-sm font-medium text-gray-700 mb-3">Top 5 峰值日</h4>
          <table className="w-full text-sm">
            <thead>
              <tr className="text-gray-400 text-xs uppercase text-left">
                <th className="py-2">日期</th>
                <th className="py-2 text-right">Token 用量</th>
                <th className="py-2 text-right">费用</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-50">
              {insight.peak_days.map((p, i) => (
                <tr key={i}>
                  <td className="py-2">{p.date}</td>
                  <td className="py-2 text-right">{formatTokens(p.tokens)}</td>
                  <td className="py-2 text-right text-red-600">${p.cost}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
