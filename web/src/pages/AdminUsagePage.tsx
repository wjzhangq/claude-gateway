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

export default function AdminUsagePage() {
  const [logs, setLogs] = useState<UsageLog[]>([])
  const [dailyStats, setDailyStats] = useState<DailyStat[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [loading, setLoading] = useState(true)
  const pageSize = 20

  useEffect(() => {
    adminGetDailyStats().then((res) => setDailyStats(res.data.stats || []))
  }, [])

  useEffect(() => {
    setLoading(true)
    adminGetUsage({ page, page_size: pageSize })
      .then((res) => {
        setLogs(res.data.logs || [])
        setTotal(res.data.total || 0)
      })
      .finally(() => setLoading(false))
  }, [page])

  // Aggregate daily stats for chart
  const chartData = dailyStats
    .reduce((acc: { date: string; tokens: number }[], s) => {
      const existing = acc.find((d) => d.date === s.date)
      if (existing) existing.tokens += s.total_tokens
      else acc.push({ date: s.date, tokens: s.total_tokens })
      return acc
    }, [])
    .slice(0, 14)
    .reverse()

  const totalPages = Math.ceil(total / pageSize)

  return (
    <div className="p-8">
      <h2 className="text-xl font-semibold text-gray-900 mb-6">使用统计（管理员）</h2>

      {chartData.length > 0 && (
        <div className="bg-white rounded-lg border border-gray-200 p-5 mb-6">
          <h3 className="text-sm font-medium text-gray-700 mb-4">每日 Token 消耗（近14天）</h3>
          <ResponsiveContainer width="100%" height={200}>
            <BarChart data={chartData}>
              <CartesianGrid strokeDasharray="3 3" stroke="#f0f0f0" />
              <XAxis dataKey="date" tick={{ fontSize: 10 }} />
              <YAxis tick={{ fontSize: 11 }} />
              <Tooltip />
              <Bar dataKey="tokens" fill="#6366f1" radius={[3, 3, 0, 0]} />
            </BarChart>
          </ResponsiveContainer>
        </div>
      )}

      <div className="bg-white rounded-lg border border-gray-200">
        <div className="px-6 py-4 border-b border-gray-200">
          <h3 className="text-sm font-medium text-gray-700">请求记录（共 {total} 条）</h3>
        </div>
        {loading ? (
          <div className="p-6 text-sm text-gray-400">加载中...</div>
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
