import { useEffect, useState } from 'react'
import { getMyUsage, exportMyUsage } from '../api'
import { formatTime } from '../utils/time'

interface UsageLog {
  id: number
  model: string
  provider: string
  backend_name: string
  input_tokens: number
  output_tokens: number
  total_tokens: number
  cost_usd: number
  status_code: number
  latency_ms: number
  is_openclaw: boolean
  is_downgraded: boolean
  ua: string
  created_at: string
}

const providerColors: Record<string, string> = {
  backend: 'bg-blue-50 text-blue-700 ring-blue-100',
  aws: 'bg-green-50 text-green-700 ring-green-100',
  kimi: 'bg-purple-50 text-purple-700 ring-purple-100',
  minimax: 'bg-orange-50 text-orange-700 ring-orange-100',
}

function ProviderBadge({ provider }: { provider: string }) {
  const cls = providerColors[provider] || 'bg-gray-50 text-gray-700 ring-gray-100'
  return (
    <span className={`inline-flex items-center px-1.5 py-0.5 rounded text-[10px] font-semibold ring-1 ${cls}`}>
      {provider}
    </span>
  )
}

function SkeletonRow() {
  return (
    <tr>
      {[130, 50, 70, 70, 80, 70, 60, 60, 100].map((w, i) => (
        <td key={i} className="px-4 py-3.5">
          <div className="skeleton h-3.5 rounded" style={{ width: w }} />
        </td>
      ))}
    </tr>
  )
}

export default function UsagePage() {
  const [logs, setLogs] = useState<UsageLog[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [loading, setLoading] = useState(true)
  const [exporting, setExporting] = useState(false)
  const [providerFilter, setProviderFilter] = useState('')
  const [expandedId, setExpandedId] = useState<number | null>(null)
  const pageSize = 20

  const load = (p: number) => {
    setLoading(true)
    const params: Record<string, string | number> = { page: p, page_size: pageSize }
    if (providerFilter) params.backend = providerFilter
    getMyUsage(params)
      .then((res) => {
        setLogs(res.data.logs || [])
        setTotal(res.data.total || 0)
      })
      .catch(() => {})
      .finally(() => setLoading(false))
  }

  const handleExport = () => {
    const date = new Date().toISOString().slice(0, 10)
    setExporting(true)
    exportMyUsage({ date })
      .then((res) => {
        const url = URL.createObjectURL(new Blob([res.data]))
        const a = document.createElement('a')
        a.href = url
        a.download = `usage_${date}.csv`
        a.click()
        URL.revokeObjectURL(url)
      })
      .catch(() => {})
      .finally(() => setExporting(false))
  }

  useEffect(() => { load(page) }, [page, providerFilter])

  const totalPages = Math.ceil(total / pageSize)

  return (
    <div className="p-8">
      <div className="mb-7 flex items-start justify-between">
        <div>
          <h2 className="text-xl font-bold text-gray-900">使用统计</h2>
          <p className="text-sm text-gray-400 mt-0.5">查看你的 API 调用记录</p>
        </div>
        <div className="flex items-center gap-3">
          <select
            value={providerFilter}
            onChange={(e) => { setProviderFilter(e.target.value); setPage(1) }}
            className="px-3 py-1.5 text-sm border border-gray-200 rounded-lg bg-white"
          >
            <option value="">全部渠道</option>
            <option value="backend">Backend</option>
            <option value="aws">AWS</option>
            <option value="kimi">Kimi</option>
            <option value="minimax">MiniMax</option>
          </select>
          <button
            onClick={handleExport}
            disabled={exporting}
            className="px-3.5 py-1.5 text-sm border border-gray-200 rounded-lg hover:bg-gray-50 disabled:opacity-40 transition-colors"
          >
            {exporting ? '导出中…' : '导出今日 CSV'}
          </button>
        </div>
      </div>

      <div className="bg-white rounded-xl border border-gray-100 shadow-sm overflow-hidden">
        <div className="px-6 py-4 border-b border-gray-100 flex items-center justify-between">
          <h3 className="text-sm font-semibold text-gray-700">请求记录</h3>
          <span className="text-xs text-gray-400">共 {total} 条</span>
        </div>
        <table className="w-full text-sm">
          <thead className="bg-gray-50/80">
            <tr>
              {['模型', '渠道', '输入', '输出', '费用', '状态', 'UA', '时间'].map((h) => (
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
                <td colSpan={8} className="px-4 py-10 text-center text-sm text-gray-400">暂无数据</td>
              </tr>
            ) : (
              logs.map((log) => (
                <>
                  <tr
                    key={log.id}
                    className="hover:bg-gray-50/50 transition-colors cursor-pointer"
                    onClick={() => setExpandedId(expandedId === log.id ? null : log.id)}
                  >
                    <td className="px-4 py-3.5 font-mono text-xs text-gray-600">
                      {log.model}
                      {log.is_openclaw && <span className="ml-1.5 inline-flex items-center px-1.5 py-0.5 rounded text-[10px] font-semibold bg-orange-50 text-orange-600 ring-1 ring-orange-100">OC</span>}
                      {log.is_downgraded && <span className="ml-1.5 inline-flex items-center px-1.5 py-0.5 rounded text-[10px] font-semibold bg-blue-50 text-blue-600 ring-1 ring-blue-100">降级</span>}
                    </td>
                    <td className="px-4 py-3.5"><ProviderBadge provider={log.provider} /></td>
                    <td className="px-4 py-3.5 text-gray-600">{log.input_tokens.toLocaleString()}</td>
                    <td className="px-4 py-3.5 text-gray-600">{log.output_tokens.toLocaleString()}</td>
                    <td className={`px-4 py-3.5 font-medium ${log.cost_usd > 0.1 ? 'text-red-600' : log.cost_usd > 0.01 ? 'text-orange-600' : 'text-gray-800'}`}>
                      ${log.cost_usd.toFixed(4)}
                    </td>
                    <td className="px-4 py-3.5">
                      <span className={`inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium ring-1 ${
                        log.status_code === 200
                          ? 'bg-green-50 text-green-700 ring-green-100'
                          : 'bg-red-50 text-red-700 ring-red-100'
                      }`}>
                        {log.status_code}
                      </span>
                    </td>
                    <td className="px-4 py-3.5 text-gray-600 text-xs font-mono">{log.ua}</td>
                    <td className="px-4 py-3.5 text-gray-400 text-xs">{formatTime(log.created_at)}</td>
                  </tr>
                  {expandedId === log.id && (
                    <tr key={`${log.id}-detail`} className="bg-gray-50/60">
                      <td colSpan={8} className="px-6 py-3">
                        <div className="flex gap-6 text-xs text-gray-500">
                          <span>总Token: <b className="text-gray-700">{log.total_tokens.toLocaleString()}</b></span>
                          {log.backend_name && <span>后端: <b className="text-gray-700">{log.backend_name}</b></span>}
                          <span>延迟: <b className="text-gray-700">{log.latency_ms}ms</b></span>
                        </div>
                      </td>
                    </tr>
                  )}
                </>
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
