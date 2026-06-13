import { useEffect, useState } from 'react'
import { adminGetAWSUserDailyCost, adminGetAWSUserMonthlyCost } from '../api'
import { toDateStr } from '../utils/time'
import {
  BarChart, Bar, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer, Cell,
} from 'recharts'

interface UserCost {
  user_id: number
  itcode: string
  requests: number
  total_tokens: number
  cost_usd: number
}

interface CostResult {
  users: UserCost[]
  total_cost: number
  total_requests: number
}

function SkeletonRow() {
  return (
    <tr>
      {[40, 100, 70, 80, 80].map((w, i) => (
        <td key={i} className="px-4 py-3.5">
          <div className="skeleton h-3.5 rounded" style={{ width: w }} />
        </td>
      ))}
    </tr>
  )
}

const COLORS = [
  '#F59E0B', '#FBBF24', '#FCD34D', '#FDE68A', '#FEF3C7',
  '#D97706', '#B45309', '#92400E', '#78350F', '#451A03',
  '#6366F1', '#818CF8', '#A5B4FC', '#10B981', '#34D399',
]

function toMonthStr(d: Date) {
  return d.toISOString().slice(0, 7)
}

export default function AdminAWSUserDailyPage() {
  const [tab, setTab] = useState<'daily' | 'monthly'>('monthly')
  const [date, setDate] = useState(() => toDateStr(new Date()))
  const [month, setMonth] = useState(() => toMonthStr(new Date()))
  const [data, setData] = useState<CostResult | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    setLoading(true)
    const req = tab === 'daily'
      ? adminGetAWSUserDailyCost({ date })
      : adminGetAWSUserMonthlyCost({ month })
    req
      .then((res) => setData(res.data))
      .catch(() => setData(null))
      .finally(() => setLoading(false))
  }, [tab, date, month])

  const shiftDate = (days: number) => {
    setDate((d) => toDateStr(new Date(new Date(d).getTime() + days * 86400000)))
  }
  const shiftMonth = (months: number) => {
    setMonth((m) => {
      const d = new Date(m + '-01')
      d.setMonth(d.getMonth() + months)
      return toMonthStr(d)
    })
  }

  const isToday = date === toDateStr(new Date())
  const isThisMonth = month === toMonthStr(new Date())
  const totalCost = data?.total_cost ?? 0
  const users = data?.users ?? []

  const chartData = users.slice(0, 15).map((u) => ({
    name: u.itcode || `uid:${u.user_id}`,
    cost: +u.cost_usd.toFixed(4),
  }))

  return (
    <div className="p-8">
      <div className="flex items-center gap-4 mb-7">
        <div>
          <h2 className="text-xl font-bold text-gray-900">AWS 用户费用排行</h2>
          <p className="text-sm text-gray-400 mt-0.5">AWS Bedrock 用户费用统计</p>
        </div>

        <div className="flex items-center gap-1 bg-gray-100 rounded-xl p-1 ml-auto">
          <button
            onClick={() => setTab('monthly')}
            className={`px-4 py-1.5 rounded-lg text-sm font-medium transition-colors ${
              tab === 'monthly' ? 'bg-white text-gray-900 shadow-sm' : 'text-gray-500 hover:text-gray-700'
            }`}
          >
            本月
          </button>
          <button
            onClick={() => setTab('daily')}
            className={`px-4 py-1.5 rounded-lg text-sm font-medium transition-colors ${
              tab === 'daily' ? 'bg-white text-gray-900 shadow-sm' : 'text-gray-500 hover:text-gray-700'
            }`}
          >
            按日
          </button>
        </div>

        {tab === 'daily' ? (
          <div className="flex items-center gap-1.5 bg-white border border-gray-200 rounded-xl px-2 py-1.5 shadow-sm">
            <button
              onClick={() => shiftDate(-1)}
              className="w-7 h-7 flex items-center justify-center rounded-lg text-gray-500 hover:bg-gray-100 transition-colors text-sm font-medium"
            >‹</button>
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
            >›</button>
          </div>
        ) : (
          <div className="flex items-center gap-1.5 bg-white border border-gray-200 rounded-xl px-2 py-1.5 shadow-sm">
            <button
              onClick={() => shiftMonth(-1)}
              className="w-7 h-7 flex items-center justify-center rounded-lg text-gray-500 hover:bg-gray-100 transition-colors text-sm font-medium"
            >‹</button>
            <input
              type="month"
              value={month}
              onChange={(e) => setMonth(e.target.value)}
              className="px-2 py-0.5 text-sm text-gray-700 focus:outline-none bg-transparent"
            />
            <button
              onClick={() => shiftMonth(1)}
              disabled={isThisMonth}
              className="w-7 h-7 flex items-center justify-center rounded-lg text-gray-500 hover:bg-gray-100 disabled:opacity-30 transition-colors text-sm font-medium"
            >›</button>
          </div>
        )}
      </div>

      <div className="grid grid-cols-3 gap-4 mb-6">
        <div className="bg-white rounded-xl border border-gray-100 px-5 py-4 shadow-sm">
          <p className="text-xs font-semibold text-gray-400 uppercase tracking-wide mb-1">
            {tab === 'daily' ? '当日请求' : '本月请求'}
          </p>
          <p className="text-xl font-bold text-gray-900">{data?.total_requests?.toLocaleString() ?? '—'}</p>
        </div>
        <div className="bg-white rounded-xl border border-gray-100 px-5 py-4 shadow-sm">
          <p className="text-xs font-semibold text-gray-400 uppercase tracking-wide mb-1">
            {tab === 'daily' ? '当日总费用' : '本月总费用'}
          </p>
          <p className="text-xl font-bold text-amber-600">${totalCost.toFixed(4)}</p>
        </div>
        <div className="bg-white rounded-xl border border-gray-100 px-5 py-4 shadow-sm">
          <p className="text-xs font-semibold text-gray-400 uppercase tracking-wide mb-1">活跃用户</p>
          <p className="text-xl font-bold text-gray-900">{users.length}</p>
        </div>
      </div>

      {chartData.length > 0 && (
        <div className="bg-white rounded-xl border border-gray-100 p-5 mb-6 shadow-sm">
          <h3 className="text-sm font-semibold text-gray-700 mb-4">用户费用 Top 15 (USD)</h3>
          <ResponsiveContainer width="100%" height={280}>
            <BarChart data={chartData} layout="vertical" margin={{ left: 20 }}>
              <CartesianGrid strokeDasharray="3 3" stroke="#f0f0f0" />
              <XAxis type="number" tick={{ fontSize: 11 }} tickFormatter={(v) => `$${v}`} />
              <YAxis type="category" dataKey="name" tick={{ fontSize: 11 }} width={100} />
              <Tooltip
                contentStyle={{ borderRadius: 8, border: '1px solid #e5e7eb', fontSize: 12 }}
                formatter={(value?: number) => [`$${(value ?? 0).toFixed(4)}`, '费用']}
              />
              <Bar dataKey="cost" name="费用" radius={[0, 3, 3, 0]}>
                {chartData.map((_, i) => (
                  <Cell key={i} fill={COLORS[i % COLORS.length]} />
                ))}
              </Bar>
            </BarChart>
          </ResponsiveContainer>
        </div>
      )}

      <div className="bg-white rounded-xl border border-gray-100 shadow-sm overflow-hidden">
        <div className="px-6 py-4 border-b border-gray-100 flex items-center justify-between">
          <h3 className="text-sm font-semibold text-gray-700">
            {tab === 'daily' ? `${date} AWS 用户费用排行` : `${month} AWS 月度费用排行`}
          </h3>
          <span className="text-xs text-gray-400">Top 20</span>
        </div>
        <table className="w-full text-sm">
          <thead className="bg-gray-50/80">
            <tr>
              {['排名', '用户', '请求数', '总 Token', '总费用'].map((h) => (
                <th key={h} className="px-4 py-3 text-left text-xs font-semibold text-gray-400 uppercase tracking-wide">
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-50">
            {loading ? (
              Array.from({ length: 5 }).map((_, i) => <SkeletonRow key={i} />)
            ) : users.length === 0 ? (
              <tr>
                <td colSpan={5} className="px-4 py-10 text-center text-sm text-gray-400">暂无数据</td>
              </tr>
            ) : (
              users.map((u, idx) => (
                <tr key={u.user_id} className="hover:bg-gray-50/50 transition-colors">
                  <td className="px-4 py-3.5">
                    <span className={`inline-flex items-center justify-center w-6 h-6 rounded-full text-xs font-bold ${
                      idx < 3 ? 'bg-amber-100 text-amber-700' : 'bg-gray-100 text-gray-500'
                    }`}>
                      {idx + 1}
                    </span>
                  </td>
                  <td className="px-4 py-3.5 font-medium text-gray-800">{u.itcode || `uid:${u.user_id}`}</td>
                  <td className="px-4 py-3.5 text-gray-700">{u.requests.toLocaleString()}</td>
                  <td className="px-4 py-3.5 font-medium text-gray-800">{u.total_tokens.toLocaleString()}</td>
                  <td className="px-4 py-3.5 font-medium text-amber-700">${u.cost_usd.toFixed(4)}</td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>
    </div>
  )
}
