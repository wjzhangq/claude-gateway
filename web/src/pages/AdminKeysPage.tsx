import { useEffect, useRef, useState } from 'react'
import { adminListKeys, adminRenameKey, adminTransferKey, adminSearchUsers, adminSwitchKeyChannel, adminCreateKey } from '../api'
import { formatTime, formatDate } from '../utils/time'

interface APIKeyWithUser {
  id: number
  user_id: number
  user_itcode: string
  user_name: string
  user_aws_enabled: boolean
  name: string
  key: string
  status: string
  channel: string
  auto_downgrade: boolean
  last_used_at: string | null
  created_at: string
  requests: number
  cost_usd: number
}

interface UserSuggestion {
  id: number
  itcode: string
  aws_enabled: boolean
}

// Reusable itcode search input with dropdown
function ItcodeSearchInput({
  value,
  onChange,
  onSelect,
  placeholder,
  className,
}: {
  value: string
  onChange: (v: string) => void
  onSelect: (u: UserSuggestion) => void
  placeholder?: string
  className?: string
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
    if (timer.current) clearTimeout(timer.current)
    timer.current = setTimeout(() => search(v), 250)
  }

  const handleSelect = (u: UserSuggestion) => {
    onChange(u.itcode)
    onSelect(u)
    setSuggestions([])
    setOpen(false)
  }

  // Close on outside click
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
        placeholder={placeholder || '输入 itcode 搜索用户'}
        className={className || 'w-48 px-3.5 py-2 border border-gray-200 rounded-xl text-sm bg-gray-50 focus:bg-white focus:outline-none focus:ring-2 focus:ring-red-500/30 focus:border-red-400 transition-all'}
      />
      {open && suggestions.length > 0 && (
        <ul className="absolute z-50 left-0 top-full mt-1 w-full bg-white border border-gray-200 rounded-xl shadow-lg overflow-hidden max-h-52 overflow-y-auto">
          {suggestions.map((u) => (
            <li
              key={u.id}
              onMouseDown={() => handleSelect(u)}
              className="px-3.5 py-2 text-sm cursor-pointer hover:bg-red-50 hover:text-red-700 transition-colors flex items-center justify-between"
            >
              <span>{u.itcode}</span>
              {u.aws_enabled && <span className="text-[10px] font-semibold text-amber-600 bg-amber-50 px-1.5 py-0.5 rounded ring-1 ring-amber-100">AWS</span>}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

function SkeletonRow() {
  return (
    <tr>
      {[80, 80, 120, 60, 60, 60, 80, 70, 90, 110, 130].map((w, i) => (
        <td key={i} className="px-4 py-3.5">
          <div className="skeleton h-3.5 rounded" style={{ width: w }} />
        </td>
      ))}
    </tr>
  )
}

export default function AdminKeysPage() {
  const [keys, setKeys] = useState<APIKeyWithUser[]>([])
  const [loading, setLoading] = useState(true)
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const pageSize = 20

  // Filter state
  const [filterItcode, setFilterItcode] = useState('')
  const [filterUserId, setFilterUserId] = useState('')

  const [renamingId, setRenamingId] = useState<number | null>(null)
  const [renameValue, setRenameValue] = useState('')
  const [transferId, setTransferId] = useState<number | null>(null)
  const [transferTarget, setTransferTarget] = useState('')
  const [transferring, setTransferring] = useState(false)
  const [revealedId, setRevealedId] = useState<number | null>(null)
  const [copied, setCopied] = useState<number | null>(null)

  // Create key form state
  const [createItcode, setCreateItcode] = useState('')
  const [createUserId, setCreateUserId] = useState('')
  const [createUserAWSEnabled, setCreateUserAWSEnabled] = useState(false)
  const [createName, setCreateName] = useState('')
  const [createAWS, setCreateAWS] = useState(false)
  const [creating, setCreating] = useState(false)
  const [createError, setCreateError] = useState('')
  const [createSuccess, setCreateSuccess] = useState('')

  const load = (p = page, uid = filterUserId) => {
    setLoading(true)
    const params: Record<string, string | number> = { page: p, page_size: pageSize }
    if (uid) params.user_id = Number(uid)
    adminListKeys(params)
      .then((res) => { setKeys(res.data.keys || []); setTotal(res.data.total || 0) })
      .catch(() => {})
      .finally(() => setLoading(false))
  }

  useEffect(() => { load() }, [])
  useEffect(() => { load(page, filterUserId) }, [page])

  const handleFilter = () => { setPage(1); load(1, filterUserId) }
  const handleFilterClear = () => { setFilterItcode(''); setFilterUserId(''); setPage(1); load(1, '') }

  const handleCopy = (id: number, key: string) => {
    navigator.clipboard.writeText(key)
    setCopied(id)
    setTimeout(() => setCopied(null), 2000)
  }

  const handleRename = async (id: number) => {
    if (!renameValue.trim()) return
    await adminRenameKey(id, renameValue.trim())
    setRenamingId(null)
    load()
  }

  const handleTransfer = async () => {
    if (!transferId || !transferTarget.trim()) return
    setTransferring(true)
    try {
      await adminTransferKey(transferId, transferTarget.trim())
      setTransferId(null)
      load()
    } finally {
      setTransferring(false)
    }
  }

  const handleSwitchChannel = async (k: APIKeyWithUser) => {
    const newCh = k.channel === 'aws' ? 'backend' : 'aws'
    if (!confirm(`确认将 Key "${k.name || k.key.slice(0, 12)}" 切换到 ${newCh === 'aws' ? 'AWS' : 'Backend'} 渠道？`)) return
    await adminSwitchKeyChannel(k.id, newCh)
    load()
  }

  const handleCreate = async () => {
    if (!createUserId) { setCreateError('请搜索并选择用户'); return }
    setCreating(true)
    setCreateError('')
    setCreateSuccess('')
    try {
      const res = await adminCreateKey({
        user_id: Number(createUserId),
        name: createName.trim() || undefined,
        channel: createAWS ? 'aws' : 'backend',
      })
      const key = res.data.key
      setCreateSuccess(`已创建 Key: ${key?.key ?? ''}`)
      setCreateName('')
      setCreateAWS(false)
      load(1)
      setPage(1)
    } catch (e: unknown) {
      const msg = (e as { response?: { data?: { error?: string } } })?.response?.data?.error
      setCreateError(msg || '创建失败')
    } finally {
      setCreating(false)
    }
  }

  const totalPages = Math.ceil(total / pageSize)

  return (
    <div className="p-8">
      {transferId !== null && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50 backdrop-blur-sm" onClick={() => setTransferId(null)}>
          <div className="bg-white rounded-2xl shadow-2xl w-[400px] p-6 border border-gray-100" onClick={(e) => e.stopPropagation()}>
            <div className="flex items-center justify-between mb-5">
              <h3 className="text-base font-bold text-gray-900">转移 Key</h3>
              <button onClick={() => setTransferId(null)} className="w-7 h-7 flex items-center justify-center rounded-lg text-gray-400 hover:text-gray-600 hover:bg-gray-100 transition-colors text-sm">✕</button>
            </div>
            <div className="space-y-4">
              <div>
                <label className="block text-xs font-semibold text-gray-500 mb-1.5 uppercase tracking-wide">目标用户 Itcode</label>
                <input
                  value={transferTarget}
                  onChange={(e) => setTransferTarget(e.target.value)}
                  placeholder="输入目标用户 itcode（不存在则自动创建并激活）"
                  className="w-full px-3.5 py-2.5 border border-gray-200 rounded-xl text-sm bg-gray-50 focus:bg-white focus:outline-none focus:ring-2 focus:ring-red-500/30 focus:border-red-400 transition-all"
                />
              </div>
              <div className="flex gap-2">
                <button
                  onClick={handleTransfer}
                  disabled={transferring || !transferTarget.trim()}
                  className="flex-1 px-4 py-2.5 bg-red-600 text-white text-sm font-medium rounded-xl hover:bg-red-700 disabled:opacity-50 transition-colors"
                >
                  {transferring ? '转移中...' : '确认转移'}
                </button>
                <button onClick={() => setTransferId(null)} className="px-4 py-2.5 text-sm border border-gray-200 rounded-xl hover:bg-gray-50 transition-colors">取消</button>
              </div>
            </div>
          </div>
        </div>
      )}

      <div className="flex items-center justify-between mb-7">
        <div>
          <h2 className="text-xl font-bold text-gray-900">Key 管理</h2>
          <p className="text-sm text-gray-400 mt-0.5">查看和管理所有 API Key</p>
        </div>
      </div>

      {/* Create key panel */}
      <div className="bg-white rounded-xl border border-gray-100 p-5 mb-5 shadow-sm">
        <h3 className="text-sm font-semibold text-gray-700 mb-3">创建 Key</h3>
        <div className="flex items-center gap-2 flex-wrap">
          <ItcodeSearchInput
            value={createItcode}
            onChange={(v) => { setCreateItcode(v); if (!v) { setCreateUserId(''); setCreateUserAWSEnabled(false) }; setCreateError(''); setCreateSuccess('') }}
            onSelect={(u) => { setCreateUserId(String(u.id)); setCreateUserAWSEnabled(u.aws_enabled); setCreateAWS(false) }}
            placeholder="搜索用户 itcode"
            className="w-48 px-3.5 py-2 border border-gray-200 rounded-xl text-sm bg-gray-50 focus:bg-white focus:outline-none focus:ring-2 focus:ring-red-500/30 focus:border-red-400 transition-all"
          />
          <input
            value={createName}
            onChange={(e) => { setCreateName(e.target.value); setCreateError(''); setCreateSuccess('') }}
            placeholder="Key 名称（可选）"
            className="w-44 px-3.5 py-2 border border-gray-200 rounded-xl text-sm bg-gray-50 focus:bg-white focus:outline-none focus:ring-2 focus:ring-red-500/30 focus:border-red-400 transition-all"
          />
          {createUserAWSEnabled && (
            <label className="flex items-center gap-1.5 text-sm text-amber-700 cursor-pointer select-none">
              <input
                type="checkbox"
                checked={createAWS}
                onChange={(e) => setCreateAWS(e.target.checked)}
                className="w-3.5 h-3.5 accent-amber-500"
              />
              AWS Key
            </label>
          )}
          <button
            onClick={handleCreate}
            disabled={creating || !createUserId}
            className="px-4 py-2 bg-red-600 text-white text-sm font-medium rounded-xl hover:bg-red-700 disabled:opacity-50 transition-colors"
          >
            {creating ? '创建中...' : '创建 Key'}
          </button>
        </div>
        {createError && <p className="mt-2 text-sm text-red-600">{createError}</p>}
        {createSuccess && <p className="mt-2 text-xs text-green-600 font-mono break-all">{createSuccess}</p>}
      </div>

      <div className="mb-4 flex items-center gap-3">
        <ItcodeSearchInput
          value={filterItcode}
          onChange={(v) => { setFilterItcode(v); if (!v) setFilterUserId('') }}
          onSelect={(u) => setFilterUserId(String(u.id))}
          placeholder="按用户 itcode 筛选"
          className="w-52 px-3.5 py-2 border border-gray-200 rounded-xl text-sm bg-white focus:outline-none focus:ring-2 focus:ring-red-500/30 focus:border-red-400 transition-all"
        />
        <button onClick={handleFilter} className="px-4 py-2 bg-red-600 text-white text-sm font-medium rounded-xl hover:bg-red-700 transition-colors">查询</button>
        {filterUserId && (
          <button onClick={handleFilterClear} className="px-3 py-2 text-sm border border-gray-200 rounded-xl hover:bg-gray-50 text-gray-500 transition-colors">清除</button>
        )}
        <span className="text-sm text-gray-400">共 {total} 条</span>
      </div>

      <div className="bg-white rounded-xl border border-gray-100 shadow-sm overflow-hidden">
        <table className="w-full text-sm">
          <thead className="bg-gray-50/80">
            <tr>
              {['用户', 'Key名称', 'Key', '状态', '渠道', '请求数', '费用', '创建时间', '最后使用', '操作'].map((h) => (
                <th key={h} className="px-4 py-3 text-left text-xs font-semibold text-gray-400 uppercase tracking-wide">{h}</th>
              ))}
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-50">
            {loading ? (
              Array.from({ length: 5 }).map((_, i) => <SkeletonRow key={i} />)
            ) : keys.length === 0 ? (
              <tr><td colSpan={10} className="px-4 py-10 text-center text-sm text-gray-400">暂无数据</td></tr>
            ) : (
              keys.map((k) => (
                <tr key={k.id} className="hover:bg-gray-50/50 transition-colors">
                  <td className="px-4 py-3.5 text-xs text-gray-600">{k.user_itcode}</td>
                  <td className="px-4 py-3.5 font-medium text-gray-800">
                    {renamingId === k.id ? (
                      <div className="flex items-center gap-1.5">
                        <input
                          value={renameValue}
                          onChange={(e) => setRenameValue(e.target.value)}
                          onKeyDown={(e) => { if (e.key === 'Enter') handleRename(k.id); if (e.key === 'Escape') setRenamingId(null) }}
                          className="w-28 px-2 py-1 border border-red-300 rounded-lg text-xs focus:outline-none focus:ring-1 focus:ring-red-400"
                          autoFocus
                        />
                        <button onClick={() => handleRename(k.id)} className="text-xs text-green-600 hover:text-green-800">✓</button>
                        <button onClick={() => setRenamingId(null)} className="text-xs text-gray-400 hover:text-gray-600">✕</button>
                      </div>
                    ) : (
                      k.name || <span className="text-gray-400 italic">无名称</span>
                    )}
                  </td>
                  <td className="px-4 py-3.5 font-mono text-xs text-gray-500">
                    {revealedId === k.id ? <span className="break-all">{k.key}</span> : <span>{k.key.slice(0, 12)}...</span>}
                  </td>
                  <td className="px-4 py-3.5">
                    <span className={`inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium ring-1 ${k.status === 'active' ? 'bg-green-50 text-green-700 ring-green-100' : 'bg-gray-100 text-gray-500 ring-gray-200'}`}>
                      {k.status === 'active' ? '启用' : '禁用'}
                    </span>
                  </td>
                  <td className="px-4 py-3.5">
                    <span className={`inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium ring-1 ${
                      k.channel === 'aws'
                        ? 'bg-amber-50 text-amber-700 ring-amber-100'
                        : 'bg-blue-50 text-blue-700 ring-blue-100'
                    }`}>
                      {k.channel === 'aws' ? 'AWS' : 'Backend'}
                    </span>
                  </td>
                  <td className="px-4 py-3.5 text-gray-600 text-xs">{(k.requests || 0).toLocaleString()}</td>
                  <td className="px-4 py-3.5 text-gray-600 text-xs">${(k.cost_usd || 0).toFixed(4)}</td>
                  <td className="px-4 py-3.5 text-gray-400 text-xs">{formatDate(k.created_at)}</td>
                  <td className="px-4 py-3.5 text-gray-400 text-xs">{formatTime(k.last_used_at)}</td>
                  <td className="px-4 py-3.5">
                    <div className="flex items-center gap-3">
                      <button onClick={() => setRevealedId(revealedId === k.id ? null : k.id)} className="text-xs text-gray-400 hover:text-gray-700 transition-colors">
                        {revealedId === k.id ? '隐藏' : '查看'}
                      </button>
                      <button onClick={() => handleCopy(k.id, k.key)} className="text-xs text-gray-400 hover:text-gray-700 transition-colors">
                        {copied === k.id ? '✓ 已复制' : '复制'}
                      </button>
                      <button onClick={() => { setRenamingId(k.id); setRenameValue(k.name || '') }} className="text-xs text-blue-400 hover:text-blue-600 transition-colors">改名</button>
                      <button onClick={() => { setTransferId(k.id); setTransferTarget('') }} className="text-xs text-purple-400 hover:text-purple-600 transition-colors">转移</button>
                      {k.user_aws_enabled && (
                        <button
                          onClick={() => handleSwitchChannel(k)}
                          className={`text-xs transition-colors ${k.channel === 'aws' ? 'text-gray-400 hover:text-gray-600' : 'text-amber-500 hover:text-amber-700'}`}
                        >
                          {k.channel === 'aws' ? '切换 Backend' : '切换 AWS'}
                        </button>
                      )}
                    </div>
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>

      {totalPages > 1 && (
        <div className="flex items-center justify-between mt-4">
          <p className="text-sm text-gray-400">共 {total} 条，第 {page}/{totalPages} 页</p>
          <div className="flex items-center gap-1.5">
            <button onClick={() => setPage(1)} disabled={page === 1} className="px-2.5 py-1.5 text-xs border border-gray-200 rounded-lg hover:bg-gray-50 disabled:opacity-40 transition-colors">«</button>
            <button onClick={() => setPage(p => Math.max(1, p - 1))} disabled={page === 1} className="px-2.5 py-1.5 text-xs border border-gray-200 rounded-lg hover:bg-gray-50 disabled:opacity-40 transition-colors">‹</button>
            {Array.from({ length: Math.min(5, totalPages) }, (_, i) => {
              const start = Math.max(1, Math.min(page - 2, totalPages - 4))
              return start + i
            }).map(p => (
              <button key={p} onClick={() => setPage(p)} className={`px-2.5 py-1.5 text-xs border rounded-lg transition-colors ${p === page ? 'bg-red-600 text-white border-red-600' : 'border-gray-200 hover:bg-gray-50'}`}>{p}</button>
            ))}
            <button onClick={() => setPage(p => Math.min(totalPages, p + 1))} disabled={page === totalPages} className="px-2.5 py-1.5 text-xs border border-gray-200 rounded-lg hover:bg-gray-50 disabled:opacity-40 transition-colors">›</button>
            <button onClick={() => setPage(totalPages)} disabled={page === totalPages} className="px-2.5 py-1.5 text-xs border border-gray-200 rounded-lg hover:bg-gray-50 disabled:opacity-40 transition-colors">»</button>
          </div>
        </div>
      )}
    </div>
  )
}
