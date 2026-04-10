import { useEffect, useState } from 'react'
import { getAWSDashboard, getMyDashboard } from '../api'
import {
  BarChart, Bar, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer,
} from 'recharts'

interface DailyStat {
  date: string
  model: string
  requests: number
  input_tokens: number
  output_tokens: number
  total_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  cost_usd: number
}

interface DashboardData {
  today_requests: number
  today_cost_usd: number
  month_requests: number
  month_cost_usd: number
  today_stats: DailyStat[]
}

function StatCard({ label, value, accent }: { label: string; value: string | number; accent: 'red' | 'blue' | 'purple' | 'amber' }) {
  const colors = {
    red: 'bg-red-50 text-red-600',
    blue: 'bg-blue-50 text-blue-600',
    purple: 'bg-purple-50 text-purple-600',
    amber: 'bg-amber-50 text-amber-600',
  }
  return (
    <div className="bg-white rounded-xl border border-gray-100 px-6 py-5 shadow-sm">
      <div className={`inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium mb-3 ${colors[accent]}`}>
        {label}
      </div>
      <p className="text-2xl font-bold text-gray-900">{value}</p>
    </div>
  )
}

export default function AWSPage() {
  const [data, setData] = useState<DashboardData | null>(null)
  const [loading, setLoading] = useState(true)
  const [quota, setQuota] = useState<{
    aws_daily_limit: number
    aws_daily_used: number
    aws_daily_remaining: number
  } | null>(null)

  useEffect(() => {
    getMyDashboard()
      .then((res) => setQuota(res.data))
      .catch(() => {})

    getAWSDashboard()
      .then((res) => setData(res.data))
      .catch(() => {})
      .finally(() => setLoading(false))
  }, [])

  const chartData = (data?.today_stats ?? [])
    .reduce((acc: { model: string; cost: number }[], s) => {
      const existing = acc.find((x) => x.model === s.model)
      if (existing) existing.cost += s.cost_usd
      else acc.push({ model: s.model, cost: s.cost_usd })
      return acc
    }, [])
    .sort((a, b) => b.cost - a.cost)
    .slice(0, 10)

  return (
    <div className="p-8">
      <div className="mb-7">
        <h2 className="text-xl font-bold text-gray-900">AWS 仪表盘</h2>
        <p className="text-sm text-gray-400 mt-0.5">AWS Bedrock 渠道使用概览</p>
      </div>

      {/* OpenClaw notice */}
      <div className="mb-5 px-4 py-3 bg-amber-50 border border-amber-200 rounded-xl flex items-start gap-2.5">
        <span className="text-amber-500 mt-0.5 flex-shrink-0 text-base">&#9888;</span>
        <div className="text-sm text-amber-800">
          <span className="font-semibold">OpenClaw 提示：</span>OpenClaw 客户端已被模型官方禁用，AWS 渠道不支持 OpenClaw 请求。请使用其他客户端工具。
        </div>
      </div>

      {/* AWS Quota card */}
      {quota && quota.aws_daily_limit > 0 && (
        <div className="mb-5">
          <div className="bg-white rounded-xl border border-gray-100 px-6 py-5 shadow-sm max-w-md">
            <div className="flex items-center justify-between mb-3">
              <span className="text-xs font-semibold text-gray-500 uppercase tracking-wide">AWS 每日限额</span>
              <span className="text-xs text-gray-400">
                ${quota.aws_daily_used.toFixed(2)} / ${quota.aws_daily_limit.toFixed(2)}
              </span>
            </div>
            <div className="w-full bg-gray-100 rounded-full h-2.5 mb-2">
              <div
                className={`h-2.5 rounded-full transition-all ${
                  quota.aws_daily_remaining <= 0
                    ? 'bg-red-500'
                    : quota.aws_daily_used / quota.aws_daily_limit > 0.8
                      ? 'bg-amber-500'
                      : 'bg-green-500'
                }`}
                style={{ width: `${Math.min((quota.aws_daily_used / quota.aws_daily_limit) * 100, 100)}%` }}
              />
            </div>
            <div className="flex items-center justify-between">
              <span className="text-lg font-bold text-gray-900">
                ${quota.aws_daily_remaining.toFixed(2)}
                <span className="text-xs font-normal text-gray-400 ml-1">剩余</span>
              </span>
              {quota.aws_daily_remaining <= 0 && (
                <span className="text-xs text-red-500 font-medium">已达上限</span>
              )}
            </div>
          </div>
        </div>
      )}

      {loading ? (
        <div className="grid grid-cols-4 gap-4 mb-6">
          {[0, 1, 2, 3].map((i) => (
            <div key={i} className="bg-white rounded-xl border border-gray-100 px-6 py-5 shadow-sm">
              <div className="skeleton h-3 w-24 rounded mb-3" />
              <div className="skeleton h-7 w-20 rounded" />
            </div>
          ))}
        </div>
      ) : (
        <div className="grid grid-cols-4 gap-4 mb-6">
          <StatCard label="今日请求" value={(data?.today_requests ?? 0).toLocaleString()} accent="blue" />
          <StatCard label="今日费用" value={`$${(data?.today_cost_usd ?? 0).toFixed(4)}`} accent="red" />
          <StatCard label="本月请求" value={(data?.month_requests ?? 0).toLocaleString()} accent="purple" />
          <StatCard label="本月费用" value={`$${(data?.month_cost_usd ?? 0).toFixed(4)}`} accent="amber" />
        </div>
      )}

      {chartData.length > 0 && (
        <div className="bg-white rounded-xl border border-gray-100 p-5 mb-6 shadow-sm">
          <h3 className="text-sm font-semibold text-gray-700 mb-4">今日按模型费用（USD）</h3>
          <ResponsiveContainer width="100%" height={220}>
            <BarChart data={chartData} layout="vertical" margin={{ left: 20 }}>
              <CartesianGrid strokeDasharray="3 3" stroke="#f0f0f0" />
              <XAxis type="number" tick={{ fontSize: 11 }} tickFormatter={(v) => `$${v}`} />
              <YAxis type="category" dataKey="model" tick={{ fontSize: 10 }} width={180} />
              <Tooltip
                contentStyle={{ borderRadius: 8, border: '1px solid #e5e7eb', fontSize: 12 }}
                formatter={(v) => [`$${Number(v).toFixed(6)}`, '费用']}
              />
              <Bar dataKey="cost" fill="#F59E0B" radius={[0, 3, 3, 0]} />
            </BarChart>
          </ResponsiveContainer>
        </div>
      )}

      <div className="bg-white rounded-xl border border-gray-100 shadow-sm overflow-hidden">
        <div className="px-6 py-4 border-b border-gray-100">
          <h3 className="text-sm font-semibold text-gray-700">今日明细</h3>
        </div>
        <table className="w-full text-sm">
          <thead className="bg-gray-50/80">
            <tr>
              {['模型', '请求数', '输入 Token', '输出 Token', '缓存读', '缓存写', '费用'].map((h) => (
                <th key={h} className="px-4 py-3 text-left text-xs font-semibold text-gray-400 uppercase tracking-wide">
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-50">
            {loading ? (
              <tr><td colSpan={7} className="px-4 py-6 text-center text-sm text-gray-400">加载中...</td></tr>
            ) : !data?.today_stats?.length ? (
              <tr><td colSpan={7} className="px-4 py-10 text-center text-sm text-gray-400">今日暂无数据</td></tr>
            ) : (
              data.today_stats.map((s, i) => (
                <tr key={i} className="hover:bg-gray-50/50 transition-colors">
                  <td className="px-4 py-3.5 font-mono text-xs text-gray-600">{s.model}</td>
                  <td className="px-4 py-3.5 text-gray-600">{s.requests.toLocaleString()}</td>
                  <td className="px-4 py-3.5 text-gray-600">{s.input_tokens.toLocaleString()}</td>
                  <td className="px-4 py-3.5 text-gray-600">{s.output_tokens.toLocaleString()}</td>
                  <td className="px-4 py-3.5 text-gray-500 text-xs">{s.cache_read_tokens.toLocaleString()}</td>
                  <td className="px-4 py-3.5 text-gray-500 text-xs">{s.cache_write_tokens.toLocaleString()}</td>
                  <td className="px-4 py-3.5 font-medium text-amber-700">${s.cost_usd.toFixed(6)}</td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>
    </div>
  )
}
