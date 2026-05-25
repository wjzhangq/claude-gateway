import { useEffect, useState } from 'react'
import { adminGetUserDailyCost } from '../api'
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
  oc_cost_usd: number
}

interface DailyResult {
  users: UserCost[]
  total_cost: number
  oc_cost: number
  total_requests: number
}

function SkeletonRow() {
  return (
    <tr>
      {[40, 100, 70, 80, 80, 80, 60].map((w, i) => (
        <td key={i} className="px-4 py-3.5">
          <div className="skeleton h-3.5 rounded" style={{ width: w }} />
        </td>
      ))}
    </tr>
  )
}

const COLORS = [
  '#DC2626', '#E04040', '#E45A5A', '#E87474', '#EC8E8E',
  '#F0A8A8', '#F4C2C2', '#F8DCDC', '#FBE8E8', '#FDF2F2',
  '#6366F1', '#818CF8', '#A5B4FC', '#C7D2FE', '#E0E7FF',
  '#10B981', '#34D399', '#6EE7B7', '#A7F3D0', '#D1FAE5',
]

export default function AdminUserDailyPage() {
  const [date, setDate] = useState(() => toDateStr(new Date()))
  const [data, setData] = useState<DailyResult | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    setLoading(true)
    adminGetUserDailyCost({ date })
      .then((res) => setData(res.data))
      .catch(() => setData(null))
      .finally(() => setLoading(false))
  }, [date])

  const shiftDate = (days: number) => {
    setDate((d) => toDateStr(new Date(new Date(d).getTime() + days * 86400000)))
  }

  const isToday = date === toDateStr(new Date())
  const totalCost = data?.total_cost ?? 0
  const ocCost = data?.oc_cost ?? 0
  const ocRatio = totalCost > 0 ? ((ocCost / totalCost) * 100).toFixed(1) : '0.0'
  const users = data?.users ?? []

  const chartData = users.slice(0, 15).map((u) => ({
    name: u.itcode || `uid:${u.user_id}`,
    cost: +u.cost_usd.toFixed(4),
    oc: +u.oc_cost_usd.toFixed(4),
  }))

  return (
    <div className="p-8">
      <div className="flex items-center gap-4 mb-7">
        <div>
          <h2 className="text-xl font-bold text-gray-900">用户费用排行</h2>
          <p className="text-sm text-gray-400 mt-0.5">按日统计用户费用 Top 20</p>
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

      <div className="grid grid-cols-4 gap-4 mb-6">
        <div className="bg-white rounded-xl border border-gray-100 px-5 py-4 shadow-sm">
          <p className="text-xs font-semibold text-gray-400 uppercase tracking-wide mb-1">当日请求</p>
          <p className="text-xl font-bold text-gray-900">{data?.total_requests?.toLocaleString() ?? '—'}</p>
        </div>
        <div className="bg-white rounded-xl border border-gray-100 px-5 py-4 shadow-sm">
          <p className="text-xs font-semibold text-gray-400 uppercase tracking-wide mb-1">当日总费用</p>
          <p className="text-xl font-bold text-gray-900">${totalCost.toFixed(4)}</p>
        </div>
        <div className="bg-white rounded-xl border border-gray-100 px-5 py-4 shadow-sm">
          <p className="text-xs font-semibold text-gray-400 uppercase tracking-wide mb-1">龙虾费用</p>
          <p className="text-xl font-bold text-orange-600">${ocCost.toFixed(4)}</p>
        </div>
        <div className="bg-white rounded-xl border border-gray-100 px-5 py-4 shadow-sm">
          <p className="text-xs font-semibold text-gray-400 uppercase tracking-wide mb-1">龙虾占比</p>
          <p className="text-xl font-bold text-orange-600">{ocRatio}%</p>
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
                formatter={((value: number, name: string) => [`$${value.toFixed(4)}`, name === 'cost' ? '总费用' : '龙虾费用']) as any}
              />
              <Bar dataKey="cost" name="总费用" radius={[0, 3, 3, 0]}>
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
          <h3 className="text-sm font-semibold text-gray-700">{date} 用户费用排行</h3>
          <span className="text-xs text-gray-400">Top 20</span>
        </div>
        <table className="w-full text-sm">
          <thead className="bg-gray-50/80">
            <tr>
              {['排名', '用户', '请求数', '总 Token', '总费用', '龙虾费用', '龙虾占比'].map((h) => (
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
                <td colSpan={7} className="px-4 py-10 text-center text-sm text-gray-400">当天暂无数据</td>
              </tr>
            ) : (
              users.map((u, idx) => {
                const userOcRatio = u.cost_usd > 0 ? ((u.oc_cost_usd / u.cost_usd) * 100).toFixed(1) : '0.0'
                return (
                  <tr key={u.user_id} className="hover:bg-gray-50/50 transition-colors">
                    <td className="px-4 py-3.5">
                      <span className={`inline-flex items-center justify-center w-6 h-6 rounded-full text-xs font-bold ${
                        idx < 3 ? 'bg-red-100 text-red-700' : 'bg-gray-100 text-gray-500'
                      }`}>
                        {idx + 1}
                      </span>
                    </td>
                    <td className="px-4 py-3.5 font-medium text-gray-800">{u.itcode || `uid:${u.user_id}`}</td>
                    <td className="px-4 py-3.5 text-gray-700">{u.requests.toLocaleString()}</td>
                    <td className="px-4 py-3.5 font-medium text-gray-800">{u.total_tokens.toLocaleString()}</td>
                    <td className="px-4 py-3.5 font-medium text-gray-900">${u.cost_usd.toFixed(4)}</td>
                    <td className="px-4 py-3.5 text-orange-600">${u.oc_cost_usd.toFixed(4)}</td>
                    <td className="px-4 py-3.5">
                      {u.oc_cost_usd > 0 ? (
                        <span className="inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium bg-orange-50 text-orange-600 ring-1 ring-orange-100">
                          {userOcRatio}%
                        </span>
                      ) : (
                        <span className="text-gray-300">—</span>
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
