import { useEffect, useState } from 'react'
import { listKeys, createKey, disableKey, enableKey, deleteKey } from '../api'

interface APIKey {
  id: number
  name: string
  key: string
  status: string
  created_at: string
  expires_at: string | null
  last_used_at: string | null
  requests: number
  cost_usd: number
}

function SkeletonRow() {
  return (
    <tr>
      {[80, 160, 60, 70, 70, 90, 110, 120].map((w, i) => (
        <td key={i} className="px-4 py-3.5">
          <div className="skeleton h-3.5 rounded" style={{ width: w }} />
        </td>
      ))}
    </tr>
  )
}

export default function APIKeysPage() {
  const [keys, setKeys] = useState<APIKey[]>([])
  const [loading, setLoading] = useState(true)
  const [showCreate, setShowCreate] = useState(false)
  const [newName, setNewName] = useState('')
  const [creating, setCreating] = useState(false)
  const [newKey, setNewKey] = useState('')
  const [error, setError] = useState('')
  const [revealedId, setRevealedId] = useState<number | null>(null)
  const [copied, setCopied] = useState<number | null>(null)

  const handleCopy = (id: number, key: string) => {
    navigator.clipboard.writeText(key)
    setCopied(id)
    setTimeout(() => setCopied(null), 2000)
  }

  const load = () => {
    setLoading(true)
    listKeys()
      .then((res) => setKeys(res.data.keys || []))
      .finally(() => setLoading(false))
  }

  useEffect(() => { load() }, [])

  const handleCreate = async () => {
    if (!newName) { setError('请输入名称'); return }
    setCreating(true)
    setError('')
    try {
      const res = await createKey(newName)
      setNewKey(res.data.key?.key ?? res.data.key)
      setNewName('')
      load()
    } catch (e: unknown) {
      const msg = (e as { response?: { data?: { error?: string } } })?.response?.data?.error
      setError(msg || '创建失败')
    } finally {
      setCreating(false)
    }
  }

  const handleToggle = async (k: APIKey) => {
    if (k.status === 'active') await disableKey(k.id)
    else await enableKey(k.id)
    load()
  }

  const handleDelete = async (id: number) => {
    if (!confirm('确认删除此 API Key？')) return
    await deleteKey(id)
    load()
  }

  return (
    <div className="p-8">
      <div className="flex items-center justify-between mb-7">
        <div>
          <h2 className="text-xl font-bold text-gray-900">API Keys</h2>
          <p className="text-sm text-gray-400 mt-0.5">管理你的 API 访问密钥</p>
        </div>
        <button
          onClick={() => { setShowCreate(true); setNewKey('') }}
          className="px-4 py-2 bg-red-600 text-white text-sm font-medium rounded-xl hover:bg-red-700 shadow-sm hover:shadow-md transition-all"
        >
          + 创建 Key
        </button>
      </div>

      {showCreate && (
        <div className="mb-6 bg-white border border-gray-100 rounded-xl p-5 shadow-sm">
          <h3 className="text-sm font-semibold text-gray-700 mb-4">新建 API Key</h3>
          {newKey ? (
            <div>
              <p className="text-sm text-gray-500 mb-3">Key 已创建，请立即复制保存（仅显示一次）：</p>
              <div className="flex items-center gap-2">
                <code className="flex-1 block bg-gray-50 border border-gray-200 rounded-xl px-4 py-3 text-sm font-mono break-all text-gray-800">
                  {newKey}
                </code>
                <button
                  onClick={() => handleCopy(-1, newKey)}
                  className="px-3 py-2 text-sm bg-red-50 text-red-600 border border-red-100 rounded-xl hover:bg-red-100 transition-colors whitespace-nowrap"
                >
                  复制
                </button>
              </div>
              <button
                onClick={() => { setShowCreate(false); setNewKey('') }}
                className="mt-3 px-4 py-2 text-sm bg-gray-100 rounded-xl hover:bg-gray-200 transition-colors"
              >
                关闭
              </button>
            </div>
          ) : (
            <div className="flex gap-2">
              <input
                value={newName}
                onChange={(e) => setNewName(e.target.value)}
                placeholder="Key 名称"
                className="flex-1 px-3.5 py-2.5 border border-gray-200 rounded-xl text-sm bg-gray-50 focus:bg-white focus:outline-none focus:ring-2 focus:ring-red-500/30 focus:border-red-400 transition-all"
              />
              <button
                onClick={handleCreate}
                disabled={creating}
                className="px-4 py-2.5 bg-red-600 text-white text-sm font-medium rounded-xl hover:bg-red-700 disabled:opacity-50 transition-colors"
              >
                {creating ? '创建中...' : '确认'}
              </button>
              <button
                onClick={() => setShowCreate(false)}
                className="px-4 py-2.5 text-sm border border-gray-200 rounded-xl hover:bg-gray-50 transition-colors"
              >
                取消
              </button>
            </div>
          )}
          {error && <p className="mt-2 text-sm text-red-600">{error}</p>}
        </div>
      )}

      <div className="bg-white rounded-xl border border-gray-100 shadow-sm overflow-hidden">
        <table className="w-full text-sm">
          <thead className="bg-gray-50/80">
            <tr>
              {['名称', 'Key', '状态', '请求数', '费用', '创建时间', '最后使用', '操作'].map((h) => (
                <th key={h} className="px-4 py-3 text-left text-xs font-semibold text-gray-400 uppercase tracking-wide">
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-50">
            {loading ? (
              Array.from({ length: 3 }).map((_, i) => <SkeletonRow key={i} />)
            ) : keys.length === 0 ? (
              <tr>
                <td colSpan={8} className="px-4 py-10 text-center text-sm text-gray-400">暂无 API Key</td>
              </tr>
            ) : (
              keys.map((k) => (
                <tr key={k.id} className="hover:bg-gray-50/50 transition-colors">
                  <td className="px-4 py-3.5 font-medium text-gray-800">{k.name}</td>
                  <td className="px-4 py-3.5 font-mono text-xs text-gray-500">
                    {revealedId === k.id ? (
                      <span className="break-all">{k.key}</span>
                    ) : (
                      <span>{k.key.slice(0, 12)}...</span>
                    )}
                  </td>
                  <td className="px-4 py-3.5">
                    <span
                      className={`inline-flex items-center px-2 py-0.5 rounded-md text-xs font-medium ring-1 ${
                        k.status === 'active'
                          ? 'bg-green-50 text-green-700 ring-green-100'
                          : 'bg-gray-100 text-gray-500 ring-gray-200'
                      }`}
                    >
                      {k.status === 'active' ? '启用' : '禁用'}
                    </span>
                  </td>
                  <td className="px-4 py-3.5 text-gray-600 text-xs">{(k.requests || 0).toLocaleString()}</td>
                  <td className="px-4 py-3.5 text-gray-600 text-xs">${(k.cost_usd || 0).toFixed(4)}</td>
                  <td className="px-4 py-3.5 text-gray-400 text-xs">
                    {new Date(k.created_at).toLocaleDateString()}
                  </td>
                  <td className="px-4 py-3.5 text-gray-400 text-xs">
                    {k.last_used_at ? new Date(k.last_used_at).toLocaleString() : '—'}
                  </td>
                  <td className="px-4 py-3.5">
                    <div className="flex items-center gap-3">
                      <button
                        onClick={() => setRevealedId(revealedId === k.id ? null : k.id)}
                        className="text-xs text-gray-400 hover:text-gray-700 transition-colors"
                      >
                        {revealedId === k.id ? '隐藏' : '查看'}
                      </button>
                      <button
                        onClick={() => handleCopy(k.id, k.key)}
                        className="text-xs text-gray-400 hover:text-gray-700 transition-colors"
                      >
                        {copied === k.id ? '✓ 已复制' : '复制'}
                      </button>
                      <button
                        onClick={() => handleToggle(k)}
                        className={`text-xs transition-colors ${
                          k.status === 'active' ? 'text-amber-500 hover:text-amber-700' : 'text-green-600 hover:text-green-800'
                        }`}
                      >
                        {k.status === 'active' ? '禁用' : '启用'}
                      </button>
                      <button
                        onClick={() => handleDelete(k.id)}
                        className="text-xs text-red-400 hover:text-red-600 transition-colors"
                      >
                        删除
                      </button>
                    </div>
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>
    </div>
  )
}
