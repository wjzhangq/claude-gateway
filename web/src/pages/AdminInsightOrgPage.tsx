import { useState, useEffect } from 'react'
import { adminGetOrgList, adminUpdateOrgTag, type OrgUser } from '../api'
import { toast } from '../components/Toast'

type SaveStatus = 'idle' | 'saving' | 'saved' | 'error'
const ROLE_TAGS = ['未分类', '研发', '非研发']

export default function AdminInsightOrgPage() {
  const [users, setUsers] = useState<OrgUser[]>([])
  const [loading, setLoading] = useState(true)
  const [filter, setFilter] = useState('all')
  const [search, setSearch] = useState('')
  const [status, setStatus] = useState<Record<number, SaveStatus>>({})
  const [draft, setDraft] = useState<Record<number, Partial<OrgUser>>>({})

  const load = async () => {
    setLoading(true)
    try {
      const { data } = await adminGetOrgList()
      setUsers(data.users || [])
    } finally {
      setLoading(false)
    }
  }
  useEffect(() => { load() }, [])

  const flashStatus = (userId: number, s: SaveStatus, ms = 1500) => {
    setStatus(prev => ({ ...prev, [userId]: s }))
    if (s === 'saved' || s === 'error') {
      setTimeout(() => setStatus(prev => {
        const next = { ...prev }; delete next[userId]; return next
      }), ms)
    }
  }

  const persist = async (u: OrgUser, patch: Partial<OrgUser>) => {
    const merged = { ...u, ...patch }
    if (merged.department === u.department && merged.role_tag === u.role_tag && merged.note === u.note) return
    flashStatus(u.user_id, 'saving')
    try {
      await adminUpdateOrgTag(u.user_id, {
        department: merged.department,
        role_tag: merged.role_tag,
        note: merged.note,
      })
      setUsers(prev => prev.map(x => x.user_id === u.user_id ? merged : x))
      flashStatus(u.user_id, 'saved')
    } catch {
      flashStatus(u.user_id, 'error')
      toast('保存失败')
    }
  }

  const getValue = (u: OrgUser, field: keyof OrgUser): string => {
    const d = draft[u.user_id]
    if (d && d[field] !== undefined) return d[field] as string
    return (u[field] as string) || ''
  }
  const setDraftField = (userId: number, field: keyof OrgUser, value: string) => {
    setDraft(prev => ({ ...prev, [userId]: { ...(prev[userId] || {}), [field]: value } }))
  }
  const commitDraft = async (u: OrgUser, field: keyof OrgUser) => {
    const value = draft[u.user_id]?.[field]
    if (value === undefined) return
    await persist(u, { [field]: value } as Partial<OrgUser>)
    setDraft(prev => {
      const next = { ...prev }
      if (next[u.user_id]) {
        const inner = { ...next[u.user_id] }
        delete inner[field]
        if (Object.keys(inner).length === 0) delete next[u.user_id]
        else next[u.user_id] = inner
      }
      return next
    })
  }

  const filtered = users
    .filter(u => filter === 'all' || (u.role_tag || '未分类') === filter)
    .filter(u => !search || u.itcode.includes(search) || (u.name || '').includes(search))

  const counts = {
    total: users.length,
    dev: users.filter(u => u.role_tag === '研发').length,
    nondev: users.filter(u => u.role_tag === '非研发').length,
    untagged: users.filter(u => !u.role_tag || u.role_tag === '未分类').length,
  }

  return (
    <div className="p-6 max-w-[1200px] mx-auto">
      <h1 className="text-xl font-semibold text-gray-900 mb-4">组织管理</h1>

      <div className="grid grid-cols-4 gap-3 mb-4">
        {[
          { n: counts.total, l: '总用户', c: 'text-gray-800' },
          { n: counts.dev, l: '研发', c: 'text-green-600' },
          { n: counts.nondev, l: '非研发', c: 'text-orange-600' },
          { n: counts.untagged, l: '未分类', c: 'text-gray-400' },
        ].map(x => (
          <div key={x.l} className="bg-white rounded-xl shadow-sm p-4 text-center">
            <div className={`text-2xl font-bold ${x.c}`}>{x.n}</div>
            <div className="text-xs text-gray-400 mt-1">{x.l}</div>
          </div>
        ))}
      </div>

      <div className="flex gap-2 items-center mb-4 flex-wrap">
        {['all', '研发', '非研发', '未分类'].map(f => (
          <button
            key={f}
            onClick={() => setFilter(f)}
            className={`px-3 py-1.5 rounded-lg text-sm font-medium ${
              filter === f ? 'bg-red-600 text-white' : 'bg-white text-gray-600 border border-gray-200 hover:bg-gray-50'
            }`}
          >{f === 'all' ? '全部' : f}</button>
        ))}
        <input
          placeholder="搜索用户 (itcode / name)"
          value={search}
          onChange={e => setSearch(e.target.value)}
          className="px-3 py-1.5 border border-gray-200 rounded-lg text-sm w-56 ml-3 focus:outline-none focus:border-red-400"
        />
        <span className="text-xs text-gray-400 ml-auto">共 {filtered.length} / {users.length} · 失焦自动保存</span>
      </div>

      <div className="bg-white rounded-xl shadow-sm overflow-hidden">
        <table className="w-full text-sm">
          <thead>
            <tr className="bg-gray-50 text-gray-500 text-xs uppercase tracking-wide text-left">
              <th className="px-4 py-3 w-44">用户</th>
              <th className="px-4 py-3 w-44">部门</th>
              <th className="px-4 py-3 w-32">角色标签</th>
              <th className="px-4 py-3">备注</th>
              <th className="px-4 py-3 w-20">状态</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-50">
            {loading ? (
              <tr><td colSpan={5} className="px-4 py-10 text-center text-gray-400">加载中…</td></tr>
            ) : filtered.map(u => {
              const st = status[u.user_id]
              return (
                <tr key={u.user_id} className="hover:bg-gray-50/60">
                  <td className="px-4 py-2.5">
                    <div className="font-medium text-gray-800">{u.name || u.itcode}</div>
                    <div className="text-[11px] text-gray-400">{u.itcode}</div>
                  </td>
                  <td className="px-4 py-2.5">
                    <input
                      value={getValue(u, 'department')}
                      placeholder="—"
                      onChange={e => setDraftField(u.user_id, 'department', e.target.value)}
                      onBlur={() => commitDraft(u, 'department')}
                      onKeyDown={e => { if (e.key === 'Enter') (e.target as HTMLInputElement).blur() }}
                      className="w-full px-2 py-1 rounded border border-transparent hover:border-gray-200 focus:border-red-400 focus:outline-none"
                    />
                  </td>
                  <td className="px-4 py-2.5">
                    <select
                      value={u.role_tag || '未分类'}
                      onChange={e => persist(u, { role_tag: e.target.value })}
                      className="w-full px-2 py-1 rounded border border-gray-200 focus:border-red-400 focus:outline-none"
                    >
                      {ROLE_TAGS.map(t => <option key={t} value={t}>{t}</option>)}
                    </select>
                  </td>
                  <td className="px-4 py-2.5">
                    <input
                      value={getValue(u, 'note')}
                      placeholder="—"
                      onChange={e => setDraftField(u.user_id, 'note', e.target.value)}
                      onBlur={() => commitDraft(u, 'note')}
                      onKeyDown={e => { if (e.key === 'Enter') (e.target as HTMLInputElement).blur() }}
                      className="w-full px-2 py-1 rounded border border-transparent hover:border-gray-200 focus:border-red-400 focus:outline-none"
                    />
                  </td>
                  <td className="px-4 py-2.5 text-xs">
                    {st === 'saving' && <span className="text-yellow-600">⟳ 保存中</span>}
                    {st === 'saved' && <span className="text-green-600">✓ 已保存</span>}
                    {st === 'error' && <span className="text-red-600">✗ 失败</span>}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
    </div>
  )
}
