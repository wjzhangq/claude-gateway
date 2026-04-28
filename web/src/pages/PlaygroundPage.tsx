import { useEffect, useRef, useState } from 'react'
import { listKeys, playgroundListModels } from '../api'
import api from '../api'

interface APIKey {
  id: number
  user_id: number
  name: string
  key: string
  channel: string
  disabled: boolean
}

interface ModelInfo {
  id: string
  owned_by: string
}

interface Message {
  role: 'user' | 'assistant' | 'system'
  content: string
}

export default function PlaygroundPage() {
  const [keys, setKeys] = useState<APIKey[]>([])
  const [selectedKey, setSelectedKey] = useState<APIKey | null>(null)
  const [models, setModels] = useState<ModelInfo[]>([])
  const [selectedModel, setSelectedModel] = useState('')
  const [messages, setMessages] = useState<Message[]>([])
  const [input, setInput] = useState('')
  const [loading, setLoading] = useState(false)
  const [keysLoading, setKeysLoading] = useState(true)
  const [modelsLoading, setModelsLoading] = useState(false)
  const chatEndRef = useRef<HTMLDivElement>(null)

  // Load models for a given key
  const loadModels = (key: APIKey) => {
    setSelectedKey(key)
    setModelsLoading(true)
    setModels([])
    setSelectedModel('')
    playgroundListModels(key.key)
      .then((res) => {
        const data: ModelInfo[] = res.data.data || []
        setModels(data)
        if (data.length > 0) setSelectedModel(data[0].id)
      })
      .catch(() => {})
      .finally(() => setModelsLoading(false))
  }

  // Load own keys on mount, auto-select first
  useEffect(() => {
    setKeysLoading(true)
    listKeys()
      .then((res) => {
        const allKeys: APIKey[] = res.data.keys || []
        const active = allKeys.filter((k) => !k.disabled)
        setKeys(active)
        if (active.length > 0) {
          loadModels(active[0])
        }
      })
      .catch(() => {})
      .finally(() => setKeysLoading(false))
  }, [])

  // Scroll to bottom on new messages
  useEffect(() => {
    chatEndRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages])

  const sendMessage = async () => {
    if (!input.trim() || !selectedKey || !selectedModel) return
    const userMsg: Message = { role: 'user', content: input.trim() }
    const newMessages = [...messages, userMsg]
    setMessages(newMessages)
    setInput('')
    setLoading(true)

    try {
      const resp = await api.post(
        '/v1/chat/completions',
        {
          model: selectedModel,
          messages: newMessages.map((m) => ({ role: m.role, content: m.content })),
          max_tokens: 4096,
        },
        {
          headers: { Authorization: `Bearer ${selectedKey.key}` },
          timeout: 120000,
        }
      )
      const choice = resp.data.choices?.[0]
      if (choice?.message?.content) {
        setMessages((prev) => [...prev, { role: 'assistant', content: choice.message.content }])
      }
    } catch (err: unknown) {
      const errorMsg = (err as { response?: { data?: { error?: string | { message?: string } } } })?.response?.data?.error
      const msg = typeof errorMsg === 'string' ? errorMsg : (errorMsg as { message?: string })?.message || 'Request failed'
      setMessages((prev) => [...prev, { role: 'assistant', content: `[Error] ${msg}` }])
    } finally {
      setLoading(false)
    }
  }

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      sendMessage()
    }
  }

  return (
    <div className="p-8 h-full flex flex-col">
      <div className="mb-6">
        <h2 className="text-xl font-bold text-gray-900">Playground</h2>
        <p className="text-sm text-gray-400 mt-0.5">选择 Key 和模型进行对话测试</p>
      </div>

      {/* Config bar */}
      <div className="bg-white rounded-xl border border-gray-100 shadow-sm px-5 py-4 mb-4 flex items-center gap-4 flex-wrap">
        {/* Key select */}
        <div>
          <label className="text-xs font-semibold text-gray-500 uppercase tracking-wide block mb-1">API Key</label>
          <select
            value={selectedKey?.id || ''}
            onChange={(e) => {
              const key = keys.find((k) => k.id === Number(e.target.value))
              if (key) loadModels(key)
            }}
            disabled={keys.length === 0}
            className="w-56 px-3 py-2 border border-gray-200 rounded-lg text-sm bg-white focus:outline-none focus:ring-2 focus:ring-red-500/30 disabled:opacity-50 transition-all"
          >
            {keysLoading ? (
              <option>加载中...</option>
            ) : keys.length === 0 ? (
              <option value="">暂无可用 Key</option>
            ) : (
              keys.map((k) => (
                <option key={k.id} value={k.id}>
                  {k.name || `Key #${k.id}`} ({k.channel})
                </option>
              ))
            )}
          </select>
        </div>

        {/* Model select */}
        <div>
          <label className="text-xs font-semibold text-gray-500 uppercase tracking-wide block mb-1">Model</label>
          <select
            value={selectedModel}
            onChange={(e) => setSelectedModel(e.target.value)}
            disabled={models.length === 0}
            className="w-72 px-3 py-2 border border-gray-200 rounded-lg text-sm bg-white focus:outline-none focus:ring-2 focus:ring-red-500/30 disabled:opacity-50 transition-all"
          >
            {modelsLoading ? (
              <option>加载模型中...</option>
            ) : models.length === 0 ? (
              <option value="">请先选择 Key</option>
            ) : (
              models.map((m) => (
                <option key={m.id} value={m.id}>
                  {m.id} ({m.owned_by})
                </option>
              ))
            )}
          </select>
        </div>

        {/* Clear chat */}
        <div className="ml-auto self-end">
          <button
            onClick={() => setMessages([])}
            disabled={messages.length === 0}
            className="px-4 py-2 text-sm border border-gray-200 rounded-lg hover:bg-gray-50 disabled:opacity-40 text-gray-600 transition-colors"
          >
            清空对话
          </button>
        </div>
      </div>

      {/* Chat area */}
      <div className="flex-1 bg-white rounded-xl border border-gray-100 shadow-sm flex flex-col overflow-hidden min-h-0">
        <div className="flex-1 overflow-auto px-6 py-4 space-y-4">
          {messages.length === 0 && (
            <div className="flex items-center justify-center h-full">
              <p className="text-sm text-gray-400">
                {selectedModel ? '输入消息开始对话...' : '请选择 Key 和模型后开始'}
              </p>
            </div>
          )}
          {messages.map((msg, i) => (
            <div key={i} className={`flex ${msg.role === 'user' ? 'justify-end' : 'justify-start'}`}>
              <div
                className={`max-w-[75%] px-4 py-3 rounded-2xl text-sm whitespace-pre-wrap ${
                  msg.role === 'user'
                    ? 'bg-red-600 text-white rounded-br-md'
                    : 'bg-gray-100 text-gray-800 rounded-bl-md'
                }`}
              >
                {msg.content}
              </div>
            </div>
          ))}
          {loading && (
            <div className="flex justify-start">
              <div className="bg-gray-100 text-gray-500 px-4 py-3 rounded-2xl rounded-bl-md text-sm">
                <span className="inline-flex gap-1">
                  <span className="animate-bounce" style={{ animationDelay: '0ms' }}>.</span>
                  <span className="animate-bounce" style={{ animationDelay: '150ms' }}>.</span>
                  <span className="animate-bounce" style={{ animationDelay: '300ms' }}>.</span>
                </span>
              </div>
            </div>
          )}
          <div ref={chatEndRef} />
        </div>

        {/* Input area */}
        <div className="border-t border-gray-100 px-4 py-3 flex items-end gap-3">
          <textarea
            value={input}
            onChange={(e) => setInput(e.target.value)}
            onKeyDown={handleKeyDown}
            placeholder={selectedModel ? '输入消息... (Enter 发送)' : '请先选择模型'}
            disabled={!selectedModel || loading}
            rows={1}
            className="flex-1 px-4 py-2.5 border border-gray-200 rounded-xl text-sm resize-none focus:outline-none focus:ring-2 focus:ring-red-500/30 focus:border-red-400 disabled:opacity-50 transition-all max-h-32"
            style={{ minHeight: '42px' }}
          />
          <button
            onClick={sendMessage}
            disabled={!input.trim() || !selectedModel || loading}
            className="px-5 py-2.5 bg-red-600 text-white text-sm font-medium rounded-xl hover:bg-red-700 disabled:opacity-40 transition-colors flex-shrink-0"
          >
            发送
          </button>
        </div>
      </div>
    </div>
  )
}
