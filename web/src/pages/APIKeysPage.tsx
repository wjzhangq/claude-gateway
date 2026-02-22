import { useEffect, useState } from 'react'
import { listKeys, createKey, disableKey, enableKey, deleteKey } from '../api'

interface APIKey {
  id: number
  name: string
  key: string
  status: string
  created_at: string
  expires_at: string | null
}

export default function APIKeysPage() {
  const [keys, setKeys] = useState<APIKey[]>([])
  const [loading, setLoading] = useState(true)
  const [showCreate, setShowCreate] = useState(false)
  const [newName, setNewName] = useState('')
  const [creating, setCreating] = useState(false)
  const [newKey, setNewKey] = useState('')
  const [error, setError] = useState('')

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
      setNewKey(res.data.key)
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
      <div className="flex items-center justify-between mb-6">
        <h2 className="text-xl font-semibold text-gray-900">API Keys</h2>
        <button
          onClick={() => { setShowCreate(true); setNewKey('') }}
          className="px-4 py-2 bg-indigo-600 text-white text-sm rounded-md hover:bg-indigo-700"
        >
          创建 Key
        </button>
      </div>

      {showCreate && (
        <div className="mb-6 bg-white border border-gray-200 rounded-lg p-5">
          <h3 className="text-sm font-medium text-gray-700 mb-3">新建 API Key</h3>
          {newKey ? (
            <div>
              <p className="text-sm text-gray-600 mb-2">Key 已创建，请立即复制保存（仅显示一次）：</p>
              <code className="block bg-gray-50 border border-gray-200 rounded px-3 py-2 text-sm font-mono break-all">
                {newKey}
              </code>
              <button
                onClick={() => { setShowCreate(false); setNewKey('') }}
                className="mt-3 px-3 py-1.5 text-sm bg-gray-100 rounded hover:bg-gray-200"
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
                className="flex-1 px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500"
              />
              <button
                onClick={handleCreate}
                disabled={creating}
                className="px-4 py-2 bg-indigo-600 text-white text-sm rounded-md hover:bg-indigo-700 disabled:opacity-50"
              >
                {creating ? '创建中...' : '确认'}
              </button>
              <button
                onClick={() => setShowCreate(false)}
                className="px-4 py-2 text-sm border border-gray-300 rounded-md hover:bg-gray-50"
              >
                取消
              </button>
            </div>
          )}
          {error && <p className="mt-2 text-sm text-red-600">{error}</p>}
        </div>
      )}

      <div className="bg-white rounded-lg border border-gray-200">
        {loading ? (
          <div className="p-6 text-sm text-gray-400">加载中...</div>
        ) : keys.length === 0 ? (
          <div className="p-6 text-sm text-gray-400">暂无 API Key</div>
        ) : (
          <table className="w-full text-sm">
            <thead className="bg-gray-50">
              <tr>
                {['名称', 'Key（前缀）', '状态', '创建时间', '操作'].map((h) => (
                  <th key={h} className="px-4 py-3 text-left text-xs font-medium text-gray-500">
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100">
              {keys.map((k) => (
                <tr key={k.id}>
                  <td className="px-4 py-3 font-medium">{k.name}</td>
                  <td className="px-4 py-3 font-mono text-xs text-gray-500">
                    {k.key.slice(0, 12)}...
                  </td>
                  <td className="px-4 py-3">
                    <span
                      className={`px-2 py-0.5 rounded text-xs ${
                        k.status === 'active'
                          ? 'bg-green-50 text-green-700'
                          : 'bg-gray-100 text-gray-500'
                      }`}
                    >
                      {k.status === 'active' ? '启用' : '禁用'}
                    </span>
                  </td>
                  <td className="px-4 py-3 text-gray-400">
                    {new Date(k.created_at).toLocaleDateString()}
                  </td>
                  <td className="px-4 py-3">
                    <div className="flex gap-2">
                      <button
                        onClick={() => handleToggle(k)}
                        className="text-xs text-indigo-600 hover:underline"
                      >
                        {k.status === 'active' ? '禁用' : '启用'}
                      </button>
                      <button
                        onClick={() => handleDelete(k.id)}
                        className="text-xs text-red-500 hover:underline"
                      >
                        删除
                      </button>
                    </div>
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
