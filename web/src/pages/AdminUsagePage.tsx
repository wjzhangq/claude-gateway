import { useEffect, useState } from 'react'
import { adminGetUsage, adminGetDailyStats } from '../api'
import {
  BarChart, Bar, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer,
} from 'recharts'

interface DailyStat {
  id: number
  date: string
  user_id: number
  model: string
  requests: number
  total_tokens: number
  cost_usd: number
}

interface UsageLog {
  id: number
  user_id: number
  itcode: string
  model: string
  backend: string
  total_tokens: number
  cost_usd: number
  status_code: number
  created_at: string
}

function toDateStr(d: Date) {
  return d.toISOString().slice(0, 10)
}

function SkeletonRow() {
  return (
    <tr>
      {[80, 130, 90, 80, 70, 60, 110].map((w, i) => (
        <td key={i} className="px-4 py-3.5">
          <div className="skeleton h-3.5 rounded" style={{ width: w }} />
        </td>
      ))}
    </tr>
  )
}

export default function AdminUsagePage() {
  const [date, setDate] = useState(() => toDateStr(new Date()))
  const [logs, setLogs] = useState<UsageLog[]>([])
  const [dailyStats, setDailyStats] = useState<DailyStat[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [loading, setLoading] = useState(true)
  const pageSize = 20

  useEffect(() => {
    const end = date
    const start = toDateStr(new Date(new Date(date).getTime() - 13 * 86400000))
    adminGetDailyStats({ start_date: start, end_date: end })
      .then((res) => setDailyStats(res.data.stats || []))
  }, [date])

  useEffect(() => {
    setLoading(true)
    adminGetUsage({ page, page_size: pageSize, start_date: date, end_date: date })
      .then((res) => {
        setLogs(res.data.logs || [])
        setTotal(res.data.total || 0)
      })
      .finally(() => setLoading(false))
  }, [date, page])

  const shiftDate = (days: number) => {
    setPage(1)
    setDate((d) => toDateStr(new Date(new Date(d).getTime() + days * 86400000)))
  }

  const chartData = dailyStats
    .reduce((acc: { date: string; tokens: number }[], s) => {
      const existing = acc.find((d) => d.date === s.date)
      if (existing) existing.tokens += s.total_tokens
      else acc.push({ date: s.date, tokens: s.total_tokens })
      return acc
    }, [])
    .sort((a, b) => a.date.localeCompare(b.date))

  const totalPages = Math.ceil(total / pageSize)
  const isToday = date === toDateStr(new Date())

  return (
    <div className="p-8">
      <div className="flex items-center gap-4 mb-7">
        <div>
          <h2 className="text-xl font-bold text-gray-900">使用统计</h2>
          <p className="text-sm text-gray-400 mt-0.5">全局 API 调用记录</p>
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
            onChange={(e) => { setPage(1); setDate(e.target.value) }}
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

      {chartData.length > 0 && (
        <div className="bg-white rounded-xl border border-gray-100 p-5 mb-6 shadow-sm">
          <h3 className="text-sm font-semibold text-gray-700 mb-4">近14天 Token 消耗</h3>
          <ResponsiveContainer width="100%" height={200}>
            <BarChart data={chartData}>
              <CartesianGrid strokeDasharray="3 3" stroke="#f0f0f0" />
              <XAxis dataKey="date" tick={{ fontSize: 10 }} />
              <YAxis tick={{ fontSize: 11 }} />
              <Tooltip contentStyle={{ borderRadius: 8, border: '1px solid #e5e7eb', fontSize: 12 }} />
              <Bar dataKey="tokens" fill="#DC2626" radius={[3, 3, 0, 0]} />
            </BarChart>
          </ResponsiveContainer>
        </div>
      )}

      <div className="bg-white rounded-xl border border-gray-100 shadow-sm overflow-hidden">
        <div className="px-6 py-4 border-b border-gray-100 flex items-center justify-between">
          <h3 className="text-sm font-semibold text-gray-700">{date} 请求记录</h3>
          <span className="text-xs text-gray-400">共 {total} 条</span>
        </div>
        <table className="w-full text-sm">
          <thead className="bg-gray-50/80">
            <tr>
              {['用户', '模型', 'Backend', '总 Token', '费用', '状态', '时间'].map((h) => (
                <th key={h} className="px-4 py-3 text-left text-xs font-semibold text-gray-400 uppercase tracking-wide">
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-50">
            {loading ? (
              Array.from({ length: 5 }).map((_, i) => <SkeletonRow key={i} />)
            ) : logs.length === 0 ? (
              <tr>
                <td colSpan={7} className="px-4 py-10 text-center text-sm text-gray-400">当天暂无数据</td>
              </tr>
            ) : (
              logs.map((log) => (
                <tr key={log.id} className="hover:bg-gray-50/50 transition-colors">
                  <td className="px-4 py-3.5">
                    <span className="font-medium text-gray-800">{log.itcode || log.user_id}</span>
                  </td>
                  <td className="px-4 py-3.5 font-mono text-xs text-gray-600">{log.model}</td>
                  <td className="px-4 py-3.5 text-xs text-gray-500">{log.backend}</td>
                  <td className="px-4 py-3.5 font-medium text-gray-800">{log.total_tokens.toLocaleString()}</td>
                  <td className="px-4 py-3.5 text-gray-700">${log.cost_usd.toFixed(4)}</td>
                  <td className="px-4 py-3.5">
                    <span
                      className={`inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium ring-1 ${
                        log.status_code === 200
                          ? 'bg-green-50 text-green-700 ring-green-100'
                          : 'bg-red-50 text-red-700 ring-red-100'
                      }`}
                    >
                      {log.status_code}
                    </span>
                  </td>
                  <td className="px-4 py-3.5 text-gray-400 text-xs">
                    {new Date(log.created_at).toLocaleString()}
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </table>
        {totalPages > 1 && (
          <div className="px-6 py-4 border-t border-gray-100 flex items-center gap-3">
            <button
              onClick={() => setPage((p) => Math.max(1, p - 1))}
              disabled={page === 1}
              className="px-3.5 py-1.5 text-sm border border-gray-200 rounded-lg hover:bg-gray-50 disabled:opacity-40 transition-colors"
            >
              上一页
            </button>
            <span className="text-sm text-gray-500">{page} / {totalPages}</span>
            <button
              onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
              disabled={page === totalPages}
              className="px-3.5 py-1.5 text-sm border border-gray-200 rounded-lg hover:bg-gray-50 disabled:opacity-40 transition-colors"
            >
              下一页
            </button>
          </div>
        )}
      </div>
    </div>
  )
}
