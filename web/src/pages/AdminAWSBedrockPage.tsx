import { useEffect, useState } from 'react'
import { adminGetAWSBedrockStats } from '../api'
import { toDateStr } from '../utils/time'
import {
  BarChart, Bar, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer, Cell,
} from 'recharts'

interface BedrockStat {
  bedrock_model: string
  requests: number
  input_tokens: number
  output_tokens: number
  total_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  cost_usd: number
}

function SkeletonRow() {
  return (
    <tr>
      {[160, 70, 80, 80, 60, 60, 80].map((w, i) => (
        <td key={i} className="px-4 py-3.5">
          <div className="skeleton h-3.5 rounded" style={{ width: w }} />
        </td>
      ))}
    </tr>
  )
}

const COLORS = [
  '#F59E0B', '#6366F1', '#10B981', '#DC2626', '#8B5CF6',
  '#EC4899', '#14B8A6', '#F97316', '#06B6D4', '#84CC16',
]

export default function AdminAWSBedrockPage() {
  const [stats, setStats] = useState<BedrockStat[]>([])
  const [loading, setLoading] = useState(true)
  const today = toDateStr(new Date())
  const defaultStart = toDateStr(new Date(new Date(today).getTime() - 29 * 86400000))
  const [startDate, setStartDate] = useState(defaultStart)
  const [endDate, setEndDate] = useState(today)

  const load = () => {
    setLoading(true)
    adminGetAWSBedrockStats({ start_date: startDate, end_date: endDate })
      .then((res) => setStats(res.data.stats || []))
      .catch(() => {})
      .finally(() => setLoading(false))
  }

  useEffect(() => { load() }, [])

  const chartData = stats
    .slice()
    .sort((a, b) => b.cost_usd - a.cost_usd)
    .slice(0, 10)
    .map((s) => ({
      name: s.bedrock_model.split('/').pop()?.split(':')[0] ?? s.bedrock_model,
      cost: +s.cost_usd.toFixed(4),
      requests: s.requests,
    }))

  const totalCost = stats.reduce((s, x) => s + x.cost_usd, 0)
  const totalRequests = stats.reduce((s, x) => s + x.requests, 0)

  return (
    <div className="p-8">
      <div className="flex items-center gap-4 mb-7">
        <div>
          <h2 className="text-xl font-bold text-gray-900">Bedrock 模型统计</h2>
          <p className="text-sm text-gray-400 mt-0.5">按 Bedrock 模型维度统计 AWS 使用情况</p>
        </div>
        <div className="flex items-center gap-2 ml-auto">
          <input
            type="date"
            value={startDate}
            onChange={(e) => setStartDate(e.target.value)}
            className="px-3 py-1.5 text-sm border border-gray-200 rounded-lg focus:outline-none focus:ring-1 focus:ring-amber-400 bg-white"
          />
          <span className="text-gray-400 text-sm">—</span>
          <input
            type="date"
            value={endDate}
            onChange={(e) => setEndDate(e.target.value)}
            className="px-3 py-1.5 text-sm border border-gray-200 rounded-lg focus:outline-none focus:ring-1 focus:ring-amber-400 bg-white"
          />
          <button
            onClick={load}
            className="px-4 py-1.5 bg-amber-600 text-white text-sm font-medium rounded-lg hover:bg-amber-700 transition-colors"
          >
            查询
          </button>
        </div>
      </div>

      <div className="grid grid-cols-3 gap-4 mb-6">
        <div className="bg-white rounded-xl border border-gray-100 px-5 py-4 shadow-sm">
          <p className="text-xs font-semibold text-gray-400 uppercase tracking-wide mb-1">总请求数</p>
          <p className="text-xl font-bold text-gray-900">{totalRequests.toLocaleString()}</p>
        </div>
        <div className="bg-white rounded-xl border border-gray-100 px-5 py-4 shadow-sm">
          <p className="text-xs font-semibold text-gray-400 uppercase tracking-wide mb-1">总费用</p>
          <p className="text-xl font-bold text-amber-600">${totalCost.toFixed(4)}</p>
        </div>
        <div className="bg-white rounded-xl border border-gray-100 px-5 py-4 shadow-sm">
          <p className="text-xs font-semibold text-gray-400 uppercase tracking-wide mb-1">活跃模型</p>
          <p className="text-xl font-bold text-gray-900">{stats.length}</p>
        </div>
      </div>

      {chartData.length > 0 && (
        <div className="bg-white rounded-xl border border-gray-100 p-5 mb-6 shadow-sm">
          <h3 className="text-sm font-semibold text-gray-700 mb-4">模型费用分布（USD）</h3>
          <ResponsiveContainer width="100%" height={220}>
            <BarChart data={chartData} layout="vertical" margin={{ left: 20 }}>
              <CartesianGrid strokeDasharray="3 3" stroke="#f0f0f0" />
              <XAxis type="number" tick={{ fontSize: 11 }} tickFormatter={(v) => `$${v}`} />
              <YAxis type="category" dataKey="name" tick={{ fontSize: 10 }} width={150} />
              <Tooltip
                contentStyle={{ borderRadius: 8, border: '1px solid #e5e7eb', fontSize: 12 }}
                formatter={(v) => [`$${Number(v).toFixed(4)}`, '费用']}
              />
              <Bar dataKey="cost" radius={[0, 3, 3, 0]}>
                {chartData.map((_, i) => (
                  <Cell key={i} fill={COLORS[i % COLORS.length]} />
                ))}
              </Bar>
            </BarChart>
          </ResponsiveContainer>
        </div>
      )}

      <div className="bg-white rounded-xl border border-gray-100 shadow-sm overflow-hidden">
        <div className="px-6 py-4 border-b border-gray-100">
          <h3 className="text-sm font-semibold text-gray-700">模型明细</h3>
        </div>
        <table className="w-full text-sm">
          <thead className="bg-gray-50/80">
            <tr>
              {['Bedrock 模型', '请求数', '输入 Token', '输出 Token', '缓存读', '缓存写', '费用'].map((h) => (
                <th key={h} className="px-4 py-3 text-left text-xs font-semibold text-gray-400 uppercase tracking-wide">
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-50">
            {loading ? (
              Array.from({ length: 4 }).map((_, i) => <SkeletonRow key={i} />)
            ) : stats.length === 0 ? (
              <tr>
                <td colSpan={7} className="px-4 py-10 text-center text-sm text-gray-400">暂无数据</td>
              </tr>
            ) : (
              stats
                .slice()
                .sort((a, b) => b.cost_usd - a.cost_usd)
                .map((s, i) => (
                  <tr key={i} className="hover:bg-gray-50/50 transition-colors">
                    <td className="px-4 py-3.5 font-mono text-xs text-gray-600 max-w-xs truncate">{s.bedrock_model}</td>
                    <td className="px-4 py-3.5 text-gray-600">{s.requests.toLocaleString()}</td>
                    <td className="px-4 py-3.5 text-gray-600">{s.input_tokens.toLocaleString()}</td>
                    <td className="px-4 py-3.5 text-gray-600">{s.output_tokens.toLocaleString()}</td>
                    <td className="px-4 py-3.5 text-gray-500 text-xs">{s.cache_read_tokens.toLocaleString()}</td>
                    <td className="px-4 py-3.5 text-gray-500 text-xs">{s.cache_write_tokens.toLocaleString()}</td>
                    <td className="px-4 py-3.5 font-medium text-amber-700">${s.cost_usd.toFixed(4)}</td>
                  </tr>
                ))
            )}
          </tbody>
        </table>
      </div>
    </div>
  )
}
