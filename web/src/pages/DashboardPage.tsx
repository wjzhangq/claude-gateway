import { useEffect, useState } from 'react'
import { getMyUsage } from '../api'

interface UsageLog {
  id: number
  model: string
  input_tokens: number
  output_tokens: number
  total_tokens: number
  cost_usd: number
  status_code: number
  created_at: string
}

export default function DashboardPage() {
  const [logs, setLogs] = useState<UsageLog[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    getMyUsage({ page: 1, page_size: 5 })
      .then((res) => {
        setLogs(res.data.logs || [])
        setTotal(res.data.total || 0)
      })
      .finally(() => setLoading(false))
  }, [])

  const totalTokens = logs.reduce((s, l) => s + l.total_tokens, 0)
  const totalCost = logs.reduce((s, l) => s + l.cost_usd, 0)

  return (
    <div className="p-8">
      <h2 className="text-xl font-semibold text-gray-900 mb-6">仪表盘</h2>

      <div className="grid grid-cols-3 gap-4 mb-8">
        <StatCard label="总请求数" value={total} />
        <StatCard label="Token 消耗（近5条）" value={totalTokens.toLocaleString()} />
        <StatCard label="费用（近5条）" value={`$${totalCost.toFixed(4)}`} />
      </div>

      <div className="bg-white rounded-lg border border-gray-200">
        <div className="px-6 py-4 border-b border-gray-200">
          <h3 className="text-sm font-medium text-gray-700">最近请求</h3>
        </div>
        {loading ? (
          <div className="p-6 text-sm text-gray-400">加载中...</div>
        ) : logs.length === 0 ? (
          <div className="p-6 text-sm text-gray-400">暂无数据</div>
        ) : (
          <table className="w-full text-sm">
            <thead className="bg-gray-50">
              <tr>
                {['模型', '输入', '输出', '费用', '状态', '时间'].map((h) => (
                  <th key={h} className="px-4 py-3 text-left text-xs font-medium text-gray-500">
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100">
              {logs.map((log) => (
                <tr key={log.id}>
                  <td className="px-4 py-3 font-mono text-xs">{log.model}</td>
                  <td className="px-4 py-3">{log.input_tokens}</td>
                  <td className="px-4 py-3">{log.output_tokens}</td>
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
                  <td className="px-4 py-3 text-gray-400">
                    {new Date(log.created_at).toLocaleString()}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  )
}

function StatCard({ label, value }: { label: string; value: string | number }) {
  return (
    <div className="bg-white rounded-lg border border-gray-200 px-6 py-5">
      <p className="text-sm text-gray-500">{label}</p>
      <p className="text-2xl font-semibold text-gray-900 mt-1">{value}</p>
    </div>
  )
}
