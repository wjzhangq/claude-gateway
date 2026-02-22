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
  model: string
  total_tokens: number
  cost_usd: number
  status_code: number
  created_at: string
}

function toDateStr(d: Date) {
  return d.toISOString().slice(0, 10)
}

export default function AdminUsagePage() {
  const [date, setDate] = useState(() => toDateStr(new Date()))
  const [logs, setLogs] = useState<UsageLog[]>([])
  const [dailyStats, setDailyStats] = useState<DailyStat[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [loading, setLoading] = useState(true)
  const pageSize = 20

  // Chart: last 14 days ending at selected date
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

  // Aggregate daily stats for chart
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
      <div className="flex items-center gap-4 mb-6">
        <h2 className="text-xl font-semibold text-gray-900">使用统计（管理员）</h2>
        <div className="flex items-center gap-1 ml-auto">
          <button
            onClick={() => shiftDate(-1)}
            className="px-2 py-1 text-sm border border-gray-300 rounded hover:bg-gray-50"
          >
            ‹
          </button>
          <input
            type="date"
            value={date}
            onChange={(e) => { setPage(1); setDate(e.target.value) }}
            className="px-3 py-1 border border-gray-300 rounded text-sm focus:outline-none focus:ring-2 focus:ring-red-500"
          />
          <button
            onClick={() => shiftDate(1)}
            disabled={isToday}
            className="px-2 py-1 text-sm border border-gray-300 rounded hover:bg-gray-50 disabled:opacity-40"
          >
            ›
          </button>
        </div>
      </div>

      {chartData.length > 0 && (
        <div className="bg-white rounded-lg border border-gray-200 p-5 mb-6">
          <h3 className="text-sm font-medium text-gray-700 mb-4">近14天 Token 消耗</h3>
          <ResponsiveContainer width="100%" height={200}>
            <BarChart data={chartData}>
              <CartesianGrid strokeDasharray="3 3" stroke="#f0f0f0" />
              <XAxis dataKey="date" tick={{ fontSize: 10 }} />
              <YAxis tick={{ fontSize: 11 }} />
              <Tooltip />
              <Bar dataKey="tokens" fill="#DC2626" radius={[3, 3, 0, 0]} />
            </BarChart>
          </ResponsiveContainer>
        </div>
      )}

      <div className="bg-white rounded-lg border border-gray-200">
        <div className="px-6 py-4 border-b border-gray-200">
          <h3 className="text-sm font-medium text-gray-700">
            {date} 请求记录（共 {total} 条）
          </h3>
        </div>
        {loading ? (
          <div className="p-6 text-sm text-gray-400">加载中...</div>
        ) : logs.length === 0 ? (
          <div className="p-6 text-sm text-gray-400">当天暂无数据</div>
        ) : (
          <>
            <table className="w-full text-sm">
              <thead className="bg-gray-50">
                <tr>
                  {['用户ID', '模型', '总 Token', '费用', '状态', '时间'].map((h) => (
                    <th key={h} className="px-4 py-3 text-left text-xs font-medium text-gray-500">
                      {h}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-100">
                {logs.map((log) => (
                  <tr key={log.id}>
                    <td className="px-4 py-3 text-gray-500">{log.user_id}</td>
                    <td className="px-4 py-3 font-mono text-xs">{log.model}</td>
                    <td className="px-4 py-3 font-medium">{log.total_tokens}</td>
                    <td className="px-4 py-3">${log.cost_usd.toFixed(4)}</td>
                    <td className="px-4 py-3">
                      <span
                        className={`px-2 py-0.5 rounded text-xs ${
                          log.status_code === 200
                            ? 'bg-green-50 text-green-700'
                            : 'bg-red-50 text-red-700'
                        }`}
                      >
                        {log.status_code}
                      </span>
                    </td>
                    <td className="px-4 py-3 text-gray-400 text-xs">
                      {new Date(log.created_at).toLocaleString()}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            {totalPages > 1 && (
              <div className="px-6 py-4 border-t border-gray-100 flex items-center gap-2">
                <button
                  onClick={() => setPage((p) => Math.max(1, p - 1))}
                  disabled={page === 1}
                  className="px-3 py-1 text-sm border rounded hover:bg-gray-50 disabled:opacity-40"
                >
                  上一页
                </button>
                <span className="text-sm text-gray-500">{page} / {totalPages}</span>
                <button
                  onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
                  disabled={page === totalPages}
                  className="px-3 py-1 text-sm border rounded hover:bg-gray-50 disabled:opacity-40"
                >
                  下一页
                </button>
              </div>
            )}
          </>
        )}
      </div>
    </div>
  )
}
