import { useEffect, useRef, useState } from 'react'
import { adminGetUsage, adminGetDailyStats, adminGetUserDailyCost, adminSearchUsers, adminExportUsage } from '../api'
import { formatTime, toDateStr } from '../utils/time'
import {
  BarChart, Bar, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer,
} from 'recharts'

interface UserSuggestion {
  id: number
  itcode: string
  aws_enabled: boolean
}

function ItcodeSearchInput({
  value,
  onChange,
  onSelect,
  onClear,
  placeholder,
}: {
  value: string
  onChange: (v: string) => void
  onSelect: (u: UserSuggestion) => void
  onClear?: () => void
  placeholder?: string
}) {
  const [suggestions, setSuggestions] = useState<UserSuggestion[]>([])
  const [open, setOpen] = useState(false)
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const wrapRef = useRef<HTMLDivElement>(null)

  const search = (q: string) => {
    if (!q.trim()) { setSuggestions([]); setOpen(false); return }
    adminSearchUsers(q.trim(), 10)
      .then((res) => {
        const users: UserSuggestion[] = res.data.users || []
        setSuggestions(users)
        setOpen(users.length > 0)
      })
      .catch(() => {})
  }

  const handleChange = (v: string) => {
    onChange(v)
    if (!v && onClear) onClear()
    if (timer.current) clearTimeout(timer.current)
    timer.current = setTimeout(() => search(v), 250)
  }

  const handleSelect = (u: UserSuggestion) => {
    onChange(u.itcode)
    onSelect(u)
    setSuggestions([])
    setOpen(false)
  }

  useEffect(() => {
    const handler = (e: MouseEvent) => {
      if (wrapRef.current && !wrapRef.current.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', handler)
    return () => document.removeEventListener('mousedown', handler)
  }, [])

  return (
    <div ref={wrapRef} className="relative">
      <input
        value={value}
        onChange={(e) => handleChange(e.target.value)}
        placeholder={placeholder || '按用户 itcode 筛选'}
        className="w-48 px-3.5 py-2 border border-gray-200 rounded-xl text-sm bg-white focus:outline-none focus:ring-2 focus:ring-red-500/30 focus:border-red-400 transition-all"
      />
      {open && suggestions.length > 0 && (
        <ul className="absolute z-50 left-0 top-full mt-1 w-full bg-white border border-gray-200 rounded-xl shadow-lg overflow-hidden max-h-52 overflow-y-auto">
          {suggestions.map((u) => (
            <li
              key={u.id}
              onMouseDown={() => handleSelect(u)}
              className="px-3.5 py-2 text-sm cursor-pointer hover:bg-red-50 hover:text-red-700 transition-colors"
            >
              {u.itcode}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

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
  is_openclaw: boolean
  ua: string
  error_reason: string
  ip: string
  city: string
  is_hq: boolean
  task_type: string       // code | doc | other | '' (unanalyzed)
  work_related: number    // 1=yes 0=no -1=undetermined
  code_direction: string  // 前端/后端/... (code only)
  created_at: string
}

// Human-readable labels for the compact reason codes persisted in usage_logs.
const ERROR_REASON_LABELS: Record<string, string> = {
  auth_401: '认证失败 (401)',
  forbidden_403: '拒绝访问 (403)',
  quota_403: '配额耗尽 (403)',
  rate_limit: '限流 (429)',
  server_5xx: '服务端错误 (5xx)',
  transport: '连接失败',
  canceled: '客户端取消',
  client_4xx: '客户端错误 (4xx)',
  probe: '健康探测',
  unknown: '未知错误',
}

function errorReasonLabel(code: string): string {
  return ERROR_REASON_LABELS[code] || code
}

// After a row is classified (feature 004), its error_reason column is reused to
// hold the packed verdict "work:<reason>;doc:<activity>". isClassifyReason tells
// that packed form apart from a genuine error code so a successful analyzed row
// is not mistaken for a failure.
function isClassifyReason(reason: string): boolean {
  return reason.startsWith('work:') || reason.startsWith('doc:')
}

// parseClassifyReason pulls the work: / doc: segments back out of the packed
// error_reason column.
function parseClassifyReason(reason: string): { work: string; doc: string } {
  let work = ''
  let doc = ''
  for (const seg of reason.split(';')) {
    if (seg.startsWith('work:')) work = seg.slice(5)
    else if (seg.startsWith('doc:')) doc = seg.slice(4)
  }
  return { work, doc }
}

// TASK_TYPE_META maps a task_type to its icon + label. The icon gives an at-a-glance
// read; the label shows in the tooltip.
const TASK_TYPE_META: Record<string, { icon: string; label: string }> = {
  code: { icon: '⌨', label: '编码' },
  doc: { icon: '📄', label: '文档' },
  other: { icon: '💬', label: '其他' },
}

// CategoryCell renders the classification for one usage row: a work/non-work badge,
// a task-type icon, and a hover tooltip carrying code_direction + what the task did.
// Content is blurred until the user holds Shift (privacy guard).
function CategoryCell({ log, shiftHeld }: { log: UsageLog; shiftHeld: boolean }) {
  // work_related: 1=work, 0=non-work, -1/absent=undetermined. task_type empty ⇒
  // the row was never analyzed (continuation, unanalyzed, or pre-feature).
  const analyzed = !!log.task_type
  if (!analyzed) {
    return <span className="text-gray-300">—</span>
  }

  const { work, doc } = isClassifyReason(log.error_reason)
    ? parseClassifyReason(log.error_reason)
    : { work: '', doc: '' }
  const meta = TASK_TYPE_META[log.task_type] || { icon: '•', label: log.task_type }

  const tipLines: string[] = []
  tipLines.push(`类型：${meta.label}`)
  if (log.task_type === 'code' && log.code_direction) tipLines.push(`方向：${log.code_direction}`)
  if (work) tipLines.push(`说明：${work}`)
  if (doc) tipLines.push(`文档：${doc}`)
  const tip = tipLines.join('\n')

  const workBadge =
    log.work_related === 1
      ? { text: 'work', cls: 'bg-green-50 text-green-700 ring-green-100' }
      : log.work_related === 0
      ? { text: 'Warning', cls: 'bg-amber-50 text-amber-700 ring-amber-100' }
      : { text: 'Unknown', cls: 'bg-gray-50 text-gray-500 ring-gray-100' }

  return (
    <span
      className={`inline-flex items-center gap-1.5 cursor-default transition-all duration-150 ${
        shiftHeld ? '' : 'blur-[3px] opacity-40 select-none'
      }`}
      title={shiftHeld ? tip : undefined}
    >
      <span className="text-sm leading-none" aria-hidden>{meta.icon}</span>
      <span className={`inline-flex items-center px-1.5 py-0.5 rounded text-[10px] font-semibold ring-1 ${workBadge.cls}`}>
        {workBadge.text}
      </span>
    </span>
  )
}

function SkeletonRow() {
  return (
    <tr>
      {[80, 130, 90, 80, 70, 60, 90, 60, 110, 90].map((w, i) => (
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

  // User filter
  const [filterItcode, setFilterItcode] = useState('')
  const [filterUserId, setFilterUserId] = useState('')

  useEffect(() => {
    const end = date
    const start = toDateStr(new Date(new Date(date).getTime() - 13 * 86400000))
    const params: Record<string, string | number> = { start_date: start, end_date: end }
    if (filterUserId) params.user_id = filterUserId
    adminGetDailyStats(params)
      .then((res) => setDailyStats(res.data.stats || []))
      .catch(() => {})
  }, [date, filterUserId])

  const [dayCost, setDayCost] = useState(0)
  const [dayOcCost, setDayOcCost] = useState(0)
  const [exporting, setExporting] = useState(false)

  const handleExport = () => {
    setExporting(true)
    const params: Record<string, string | number> = { date }
    if (filterUserId) params.user_id = filterUserId
    adminExportUsage(params)
      .then((res) => {
        const url = URL.createObjectURL(res.data)
        const a = document.createElement('a')
        a.href = url
        a.download = `usage_${date}.csv`
        a.click()
        URL.revokeObjectURL(url)
      })
      .catch(() => {})
      .finally(() => setExporting(false))
  }

  useEffect(() => {
    setLoading(true)
    const params: Record<string, string | number> = { page, page_size: pageSize, start_date: date, end_date: date }
    if (filterUserId) params.user_id = filterUserId
    adminGetUsage(params)
      .then((res) => {
        setLogs(res.data.logs || [])
        setTotal(res.data.total || 0)
      })
      .catch(() => {})
      .finally(() => setLoading(false))
  }, [date, page, filterUserId])

  useEffect(() => {
    const params: Record<string, string | number> = { date }
    adminGetUserDailyCost(params)
      .then((res) => {
        setDayCost(res.data.total_cost ?? 0)
        setDayOcCost(res.data.oc_cost ?? 0)
      })
      .catch(() => {})
  }, [date])

  const [shiftHeld, setShiftHeld] = useState(false)
  useEffect(() => {
    const down = (e: KeyboardEvent) => { if (e.key === 'Shift') setShiftHeld(true) }
    const up = (e: KeyboardEvent) => { if (e.key === 'Shift') setShiftHeld(false) }
    window.addEventListener('keydown', down)
    window.addEventListener('keyup', up)
    return () => { window.removeEventListener('keydown', down); window.removeEventListener('keyup', up) }
  }, [])

  const shiftDate = (days: number) => {
    setPage(1)
    setDate((d) => toDateStr(new Date(new Date(d).getTime() + days * 86400000)))
  }

  const chartData = dailyStats
    .reduce((acc: { date: string; cost: number }[], s) => {
      const existing = acc.find((d) => d.date === s.date)
      if (existing) existing.cost += s.cost_usd
      else acc.push({ date: s.date, cost: s.cost_usd })
      return acc
    }, [])
    .sort((a, b) => a.date.localeCompare(b.date))
    .map((d) => ({ ...d, cost: +d.cost.toFixed(4) }))

  const totalPages = Math.ceil(total / pageSize)
  const isToday = date === toDateStr(new Date())
  const ocRatio = dayCost > 0 ? ((dayOcCost / dayCost) * 100).toFixed(1) : '0.0'

  return (
    <div className="p-8">
      <div className="flex items-center gap-4 mb-7">
        <div>
          <h2 className="text-xl font-bold text-gray-900">使用统计</h2>
          <p className="text-sm text-gray-400 mt-0.5">全局 API 调用记录</p>
        </div>
        <div className="ml-auto flex items-center gap-3">
          <ItcodeSearchInput
            value={filterItcode}
            onChange={(v) => { setFilterItcode(v); if (!v) { setFilterUserId(''); setPage(1) } }}
            onSelect={(u) => { setFilterUserId(String(u.id)); setPage(1) }}
            onClear={() => { setFilterUserId(''); setPage(1) }}
            placeholder="按用户 itcode 筛选"
          />
          {filterUserId && (
            <button
              onClick={() => { setFilterItcode(''); setFilterUserId(''); setPage(1) }}
              className="px-3 py-2 text-xs border border-gray-200 rounded-xl hover:bg-gray-50 text-gray-500 transition-colors"
            >
              清除
            </button>
          )}
          <button
            onClick={handleExport}
            disabled={exporting}
            className="px-3.5 py-2 text-xs border border-gray-200 rounded-xl hover:bg-gray-50 text-gray-600 transition-colors disabled:opacity-40 flex items-center gap-1.5"
          >
            <svg className="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-4l-4 4m0 0l-4-4m4 4V4" />
            </svg>
            {exporting ? '导出中…' : '导出 CSV'}
          </button>
          <div className="flex items-center gap-1.5 bg-white border border-gray-200 rounded-xl px-2 py-1.5 shadow-sm">
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
      </div>

      <div className="grid grid-cols-4 gap-4 mb-6">
        <div className="bg-white rounded-xl border border-gray-100 px-5 py-4 shadow-sm">
          <p className="text-xs font-semibold text-gray-400 uppercase tracking-wide mb-1">当日请求</p>
          <p className="text-xl font-bold text-gray-900">{total}</p>
        </div>
        <div className="bg-white rounded-xl border border-gray-100 px-5 py-4 shadow-sm">
          <p className="text-xs font-semibold text-gray-400 uppercase tracking-wide mb-1">当日费用</p>
          <p className="text-xl font-bold text-gray-900">${dayCost.toFixed(4)}</p>
        </div>
        <div className="bg-white rounded-xl border border-gray-100 px-5 py-4 shadow-sm">
          <p className="text-xs font-semibold text-gray-400 uppercase tracking-wide mb-1">龙虾费用</p>
          <p className="text-xl font-bold text-orange-600">${dayOcCost.toFixed(4)}</p>
        </div>
        <div className="bg-white rounded-xl border border-gray-100 px-5 py-4 shadow-sm">
          <p className="text-xs font-semibold text-gray-400 uppercase tracking-wide mb-1">龙虾占比</p>
          <p className="text-xl font-bold text-orange-600">{ocRatio}%</p>
        </div>
      </div>

      {chartData.length > 0 && (
        <div className="bg-white rounded-xl border border-gray-100 p-5 mb-6 shadow-sm">
          <h3 className="text-sm font-semibold text-gray-700 mb-4">近14天费用趋势 (USD)</h3>
          <ResponsiveContainer width="100%" height={200}>
            <BarChart data={chartData}>
              <CartesianGrid strokeDasharray="3 3" stroke="#f0f0f0" />
              <XAxis dataKey="date" tick={{ fontSize: 10 }} />
              <YAxis tick={{ fontSize: 11 }} tickFormatter={(v) => `$${v}`} />
              <Tooltip contentStyle={{ borderRadius: 8, border: '1px solid #e5e7eb', fontSize: 12 }} formatter={(value?: number) => [`$${(value ?? 0).toFixed(4)}`, '费用']} />
              <Bar dataKey="cost" name="费用" fill="#DC2626" radius={[3, 3, 0, 0]} />
            </BarChart>
          </ResponsiveContainer>
        </div>
      )}

      <div className="bg-white rounded-xl border border-gray-100 shadow-sm overflow-hidden">
        <div className="px-6 py-4 border-b border-gray-100 flex items-center justify-between">
          <h3 className="text-sm font-semibold text-gray-700">{date} 请求记录{filterItcode ? ` · ${filterItcode}` : ''}</h3>
          <span className="text-xs text-gray-400">共 {total} 条</span>
        </div>
        <table className="w-full text-sm">
          <thead className="bg-gray-50/80">
            <tr>
              {['用户', '模型', 'Backend', '城市', '总 Token', '费用', '状态', 'UA', '时间'].map((h) => (
                <th key={h} className="px-4 py-3 text-left text-xs font-semibold text-gray-400 uppercase tracking-wide">
                  {h}
                </th>
              ))}
              <th className="px-4 py-3 text-left text-xs font-semibold uppercase tracking-wide">
                <span className={`transition-colors duration-150 ${shiftHeld ? 'text-blue-500' : 'text-gray-400'}`}>
                  分类{shiftHeld ? '' : ' ⇧'}
                </span>
              </th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-50">
            {loading ? (
              Array.from({ length: 5 }).map((_, i) => <SkeletonRow key={i} />)
            ) : logs.length === 0 ? (
              <tr>
                <td colSpan={10} className="px-4 py-10 text-center text-sm text-gray-400">当天暂无数据</td>
              </tr>
            ) : (
              logs.map((log) => {
                const isError = log.status_code >= 400 || (!!log.error_reason && !isClassifyReason(log.error_reason))
                return (
                <tr key={log.id} className={`transition-colors ${isError ? 'bg-red-50/40 hover:bg-red-50/70' : 'hover:bg-gray-50/50'}`}>
                  <td className="px-4 py-3.5">
                    <span className="font-medium text-gray-800">{log.itcode || log.user_id}</span>
                  </td>
                  <td className="px-4 py-3.5 font-mono text-xs text-gray-600">
                    {log.model}
                    {log.is_openclaw && log.ua === 'hermesclaw' && <span className="ml-1.5 inline-flex items-center px-1.5 py-0.5 rounded text-[10px] font-semibold bg-purple-50 text-purple-600 ring-1 ring-purple-100">HC</span>}
                    {log.is_openclaw && log.ua !== 'hermesclaw' && <span className="ml-1.5 inline-flex items-center px-1.5 py-0.5 rounded text-[10px] font-semibold bg-orange-50 text-orange-600 ring-1 ring-orange-100">OC</span>}
                  </td>
                  <td className="px-4 py-3.5 text-xs text-gray-500">{log.backend}</td>
                  <td className="px-4 py-3.5 text-xs">
                    {log.city ? (
                      <span className="inline-flex items-center gap-1 text-gray-600" title={log.ip}>
                        {log.city}
                        {log.is_hq && (
                          <span className="inline-flex items-center px-1.5 py-0.5 rounded text-[10px] font-semibold bg-blue-50 text-blue-600 ring-1 ring-blue-100">总部</span>
                        )}
                      </span>
                    ) : log.is_hq ? (
                      <span className="inline-flex items-center px-1.5 py-0.5 rounded text-[10px] font-semibold bg-blue-50 text-blue-600 ring-1 ring-blue-100" title={log.ip}>总部</span>
                    ) : (
                      <span className="text-gray-300" title={log.ip}>—</span>
                    )}
                  </td>
                  <td className="px-4 py-3.5 font-medium text-gray-800">{log.total_tokens.toLocaleString()}</td>
                  <td className="px-4 py-3.5 text-gray-700">${log.cost_usd.toFixed(4)}</td>
                  <td className="px-4 py-3.5">
                    <span
                      className={`inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium ring-1 ${
                        log.status_code === 200
                          ? 'bg-green-50 text-green-700 ring-green-100'
                          : 'bg-red-50 text-red-700 ring-red-100'
                      }`}
                      title={log.error_reason && !isClassifyReason(log.error_reason) ? errorReasonLabel(log.error_reason) : undefined}
                    >
                      {log.status_code}
                    </span>
                  </td>
                  <td className="px-4 py-3.5 text-gray-600 text-xs font-mono">{log.ua}</td>
                  <td className="px-4 py-3.5 text-gray-400 text-xs">
                    {formatTime(log.created_at)}
                  </td>
                  <td className="px-4 py-3.5">
                    <CategoryCell log={log} shiftHeld={shiftHeld} />
                  </td>
                </tr>
              )})
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
