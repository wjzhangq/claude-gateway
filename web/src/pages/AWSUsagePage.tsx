import { useEffect, useState } from 'react'
import { getAWSMyUsage } from '../api'
import { formatTime } from '../utils/time'

interface AWSUsageLog {
  id: number
  model: string
  bedrock_model: string
  input_tokens: number
  output_tokens: number
  total_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  cost_usd: number
  status_code: number
  latency_ms: number
  ua: string
  created_at: string
}

function SkeletonRow() {
  return (
    <tr>
      {[130, 70, 70, 70, 70, 70, 60, 60, 100].map((w, i) => (
        <td key={i} className="px-4 py-3.5">
          <div className="skeleton h-3.5 rounded" style={{ width: w }} />
        </td>
      ))}
    </tr>
  )
}

export default function AWSUsagePage() {
  const [logs, setLogs] = useState<AWSUsageLog[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [loading, setLoading] = useState(true)
  const pageSize = 20

  const load = (p: number) => {
    setLoading(true)
    getAWSMyUsage({ page: p, page_size: pageSize })
      .then((res) => {
        setLogs(res.data.logs || [])
        setTotal(res.data.total || 0)
      })
      .catch(() => {})
      .finally(() => setLoading(false))
  }

  useEffect(() => { load(page) }, [page])

  const totalPages = Math.ceil(total / pageSize)

  return (
    <div className="p-8">
      <div className="mb-7">
        <h2 className="text-xl font-bold text-gray-900">AWS 使用统计</h2>
        <p className="text-sm text-gray-400 mt-0.5">查看你的 AWS Bedrock API 调用记录</p>
      </div>

      <div className="bg-white rounded-xl border border-gray-100 shadow-sm overflow-hidden">
        <div className="px-6 py-4 border-b border-gray-100 flex items-center justify-between">
          <h3 className="text-sm font-semibold text-gray-700">请求记录</h3>
          <span className="text-xs text-gray-400">共 {total} 条</span>
        </div>
        <table className="w-full text-sm">
          <thead className="bg-gray-50/80">
            <tr>
              {['模型', '输入', '输出', '缓存读', '缓存写', '总 Token', '费用', '状态', '时间'].map((h) => (
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
                <td colSpan={9} className="px-4 py-10 text-center text-sm text-gray-400">暂无数据</td>
              </tr>
            ) : (
              logs.map((log) => (
                <tr key={log.id} className="hover:bg-gray-50/50 transition-colors">
                  <td className="px-4 py-3.5 font-mono text-xs text-gray-600">{log.model}</td>
                  <td className="px-4 py-3.5 text-gray-600">{log.input_tokens.toLocaleString()}</td>
                  <td className="px-4 py-3.5 text-gray-600">{log.output_tokens.toLocaleString()}</td>
                  <td className="px-4 py-3.5 text-gray-500 text-xs">{log.cache_read_tokens.toLocaleString()}</td>
                  <td className="px-4 py-3.5 text-gray-500 text-xs">{log.cache_write_tokens.toLocaleString()}</td>
                  <td className="px-4 py-3.5 font-medium text-gray-800">{log.total_tokens.toLocaleString()}</td>
                  <td className="px-4 py-3.5 font-medium text-amber-700">${log.cost_usd.toFixed(6)}</td>
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
                    {formatTime(log.created_at)}
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
