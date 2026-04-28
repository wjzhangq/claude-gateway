import { useEffect, useRef, useState } from 'react'
import { adminGetPublicUsage, adminSearchUsers } from '../api'
import { formatTime, toDateStr } from '../utils/time'

interface UserSuggestion {
  id: number
  itcode: string
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
        placeholder={placeholder || 'Filter by itcode'}
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

interface UsageLog {
  id: number
  user_id: number
  itcode: string
  model: string
  backend: string
  input_tokens: number
  output_tokens: number
  total_tokens: number
  cost_usd: number
  status_code: number
  ua: string
  created_at: string
}

function SkeletonRow() {
  return (
    <tr>
      {[80, 100, 80, 90, 70, 60, 60, 110].map((w, i) => (
        <td key={i} className="px-4 py-3.5">
          <div className="skeleton h-3.5 rounded" style={{ width: w }} />
        </td>
      ))}
    </tr>
  )
}

export default function AdminPublicUsagePage() {
  const [date, setDate] = useState(() => toDateStr(new Date()))
  const [logs, setLogs] = useState<UsageLog[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [loading, setLoading] = useState(true)
  const pageSize = 20

  const [filterItcode, setFilterItcode] = useState('')
  const [filterUserId, setFilterUserId] = useState('')

  useEffect(() => {
    setLoading(true)
    const params: Record<string, string | number> = { page, page_size: pageSize, start_date: date, end_date: date }
    if (filterUserId) params.user_id = filterUserId
    adminGetPublicUsage(params)
      .then((res) => {
        setLogs(res.data.logs || [])
        setTotal(res.data.total || 0)
      })
      .catch(() => {})
      .finally(() => setLoading(false))
  }, [date, page, filterUserId])

  // Day cost
  const [dayCost, setDayCost] = useState(0)
  useEffect(() => {
    const params: Record<string, string | number> = { page: 1, page_size: 10000, start_date: date, end_date: date }
    if (filterUserId) params.user_id = filterUserId
    adminGetPublicUsage(params)
      .then((res) => {
        const all: UsageLog[] = res.data.logs || []
        setDayCost(all.reduce((s, l) => s + l.cost_usd, 0))
      })
      .catch(() => {})
  }, [date, filterUserId])

  const shiftDate = (days: number) => {
    setPage(1)
    setDate((d) => toDateStr(new Date(new Date(d).getTime() + days * 86400000)))
  }

  const totalPages = Math.ceil(total / pageSize)
  const isToday = date === toDateStr(new Date())

  return (
    <div className="p-8">
      <div className="flex items-center gap-4 mb-7">
        <div>
          <h2 className="text-xl font-bold text-gray-900">Public Usage Logs</h2>
          <p className="text-sm text-gray-400 mt-0.5">Kimi / MiniMax request logs</p>
        </div>
        <div className="ml-auto flex items-center gap-3">
          <ItcodeSearchInput
            value={filterItcode}
            onChange={(v) => { setFilterItcode(v); if (!v) { setFilterUserId(''); setPage(1) } }}
            onSelect={(u) => { setFilterUserId(String(u.id)); setPage(1) }}
            onClear={() => { setFilterUserId(''); setPage(1) }}
          />
          {filterUserId && (
            <button
              onClick={() => { setFilterItcode(''); setFilterUserId(''); setPage(1) }}
              className="px-3 py-2 text-xs border border-gray-200 rounded-xl hover:bg-gray-50 text-gray-500 transition-colors"
            >
              Clear
            </button>
          )}
          <div className="flex items-center gap-1.5 bg-white border border-gray-200 rounded-xl px-2 py-1.5 shadow-sm">
            <button
              onClick={() => shiftDate(-1)}
              className="w-7 h-7 flex items-center justify-center rounded-lg text-gray-500 hover:bg-gray-100 transition-colors text-sm font-medium"
            >
              &lsaquo;
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
              &rsaquo;
            </button>
          </div>
        </div>
      </div>

      <div className="grid grid-cols-2 gap-4 mb-6">
        <div className="bg-white rounded-xl border border-gray-100 px-5 py-4 shadow-sm">
          <p className="text-xs font-semibold text-gray-400 uppercase tracking-wide mb-1">Requests Today</p>
          <p className="text-xl font-bold text-gray-900">{total}</p>
        </div>
        <div className="bg-white rounded-xl border border-gray-100 px-5 py-4 shadow-sm">
          <p className="text-xs font-semibold text-gray-400 uppercase tracking-wide mb-1">Cost Today</p>
          <p className="text-xl font-bold text-gray-900">${dayCost.toFixed(4)}</p>
        </div>
      </div>

      <div className="bg-white rounded-xl border border-gray-100 shadow-sm overflow-hidden">
        <div className="px-6 py-4 border-b border-gray-100 flex items-center justify-between">
          <h3 className="text-sm font-semibold text-gray-700">{date} Logs{filterItcode ? ` - ${filterItcode}` : ''}</h3>
          <span className="text-xs text-gray-400">{total} records</span>
        </div>
        <table className="w-full text-sm">
          <thead className="bg-gray-50/80">
            <tr>
              {['User', 'Model', 'Provider', 'Input', 'Output', 'Cost', 'Status', 'Time'].map((h) => (
                <th key={h} className="px-4 py-3 text-left text-xs font-semibold text-gray-400 uppercase tracking-wide">{h}</th>
              ))}
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-50">
            {loading ? (
              Array.from({ length: 5 }).map((_, i) => <SkeletonRow key={i} />)
            ) : logs.length === 0 ? (
              <tr>
                <td colSpan={8} className="px-4 py-10 text-center text-sm text-gray-400">No data for this date</td>
              </tr>
            ) : (
              logs.map((log) => (
                <tr key={log.id} className="hover:bg-gray-50/50 transition-colors">
                  <td className="px-4 py-3.5 font-medium text-gray-800">{log.itcode || log.user_id}</td>
                  <td className="px-4 py-3.5 font-mono text-xs text-gray-600">{log.model}</td>
                  <td className="px-4 py-3.5">
                    <span className="inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium bg-indigo-50 text-indigo-700 ring-1 ring-indigo-100">
                      {log.backend.replace('public:', '')}
                    </span>
                  </td>
                  <td className="px-4 py-3.5 text-gray-700 tabular-nums">{log.input_tokens?.toLocaleString()}</td>
                  <td className="px-4 py-3.5 text-gray-700 tabular-nums">{log.output_tokens?.toLocaleString()}</td>
                  <td className="px-4 py-3.5 text-gray-700">${log.cost_usd.toFixed(4)}</td>
                  <td className="px-4 py-3.5">
                    <span className={`inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium ring-1 ${
                      log.status_code === 200
                        ? 'bg-green-50 text-green-700 ring-green-100'
                        : 'bg-red-50 text-red-700 ring-red-100'
                    }`}>
                      {log.status_code}
                    </span>
                  </td>
                  <td className="px-4 py-3.5 text-gray-400 text-xs">{formatTime(log.created_at)}</td>
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
              Previous
            </button>
            <span className="text-sm text-gray-500">{page} / {totalPages}</span>
            <button
              onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
              disabled={page === totalPages}
              className="px-3.5 py-1.5 text-sm border border-gray-200 rounded-lg hover:bg-gray-50 disabled:opacity-40 transition-colors"
            >
              Next
            </button>
          </div>
        )}
      </div>
    </div>
  )
}
