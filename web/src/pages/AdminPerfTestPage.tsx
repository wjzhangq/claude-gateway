import { useState, useEffect, useRef } from 'react'
import { adminStartPerfTest, adminGetPerfTestRuns, adminGetPerfTestRun, adminCancelPerfTest, adminGetPerfTestOptions } from '../api'

interface ChannelConfig {
  name: string
  model: string
  backend_name?: string
}

interface CellResult {
  channel: string
  model: string
  input_tokens: number
  max_tokens: number
  ttft_ms: number
  tpot_ms: number
  tokens_per_second: number
  actual_output_tokens: number
  total_duration_ms: number
  status: string
  error_msg?: string
}

interface TestRun {
  id: number
  created_at: string
  initiated_by: string
  status: string
  channels: string
  input_sizes: string
  output_sizes: string
  total_cells: number
  completed_cells: number
  error_msg?: string
}

interface ChannelOption {
  name: string
  label: string
  models?: string[]
  backends?: string[]
}

const DEFAULT_INPUT_SIZES = [256, 1024, 2048]
const DEFAULT_OUTPUT_SIZES = [128, 512]

export default function AdminPerfTestPage() {
  const [tab, setTab] = useState<'config' | 'running' | 'history'>('config')
  const [channelOptions, setChannelOptions] = useState<ChannelOption[]>([])
  const [selectedChannels, setSelectedChannels] = useState<ChannelConfig[]>([])
  const [inputSizes, setInputSizes] = useState<number[]>(DEFAULT_INPUT_SIZES)
  const [outputSizes, setOutputSizes] = useState<number[]>(DEFAULT_OUTPUT_SIZES)
  const [inputSizesText, setInputSizesText] = useState(DEFAULT_INPUT_SIZES.join(', '))
  const [outputSizesText, setOutputSizesText] = useState(DEFAULT_OUTPUT_SIZES.join(', '))

  const [runningId, setRunningId] = useState<number | null>(null)
  const [progress, setProgress] = useState({ completed: 0, total: 0, channel: '', input: 0, output: 0 })
  const [results, setResults] = useState<CellResult[]>([])

  const [history, setHistory] = useState<TestRun[]>([])
  const [expandedRun, setExpandedRun] = useState<number | null>(null)
  const [expandedResults, setExpandedResults] = useState<CellResult[]>([])

  const eventSourceRef = useRef<EventSource | null>(null)

  useEffect(() => {
    loadOptions()
    loadHistory()
  }, [])

  useEffect(() => {
    return () => {
      eventSourceRef.current?.close()
    }
  }, [])

  const loadOptions = async () => {
    try {
      const res = await adminGetPerfTestOptions()
      setChannelOptions(res.data.channels || [])
    } catch { /* ignore */ }
  }

  const loadHistory = async () => {
    try {
      const res = await adminGetPerfTestRuns(30)
      setHistory(res.data.runs || [])
    } catch { /* ignore */ }
  }

  const toggleChannel = (name: string) => {
    setSelectedChannels(prev => {
      const exists = prev.find(c => c.name === name)
      if (exists) return prev.filter(c => c.name !== name)
      const opt = channelOptions.find(o => o.name === name)
      const defaultModel = opt?.models?.[0] || ''
      const defaultBackend = opt?.backends?.[0] || ''
      return [...prev, { name, model: defaultModel, backend_name: name === 'backend' ? defaultBackend : undefined }]
    })
  }

  const updateChannelModel = (name: string, model: string) => {
    setSelectedChannels(prev =>
      prev.map(c => c.name === name ? { ...c, model } : c)
    )
  }

  const updateChannelBackend = (name: string, backendName: string) => {
    setSelectedChannels(prev =>
      prev.map(c => c.name === name ? { ...c, backend_name: backendName } : c)
    )
  }

  const parseNumberList = (text: string): number[] => {
    return text.split(/[,，\s]+/).map(s => parseInt(s.trim())).filter(n => !isNaN(n) && n > 0)
  }

  const startTest = async () => {
    const inputs = parseNumberList(inputSizesText)
    const outputs = parseNumberList(outputSizesText)
    setInputSizes(inputs)
    setOutputSizes(outputs)

    if (selectedChannels.length === 0 || inputs.length === 0 || outputs.length === 0) return

    try {
      const res = await adminStartPerfTest({
        channels: selectedChannels,
        input_sizes: inputs,
        output_sizes: outputs,
      })
      const runId = res.data.id
      setRunningId(runId)
      setProgress({ completed: 0, total: res.data.total_cells, channel: '', input: 0, output: 0 })
      setResults([])
      setTab('running')
      connectSSE(runId)
    } catch { /* error toast handled by interceptor */ }
  }

  const connectSSE = (runId: number) => {
    eventSourceRef.current?.close()
    const es = new EventSource(`/admin/api/perftest/run/${runId}/stream`, { withCredentials: true })
    eventSourceRef.current = es

    es.addEventListener('progress', (e: MessageEvent) => {
      const data = JSON.parse(e.data)
      setProgress(data)
    })

    es.addEventListener('result', (e: MessageEvent) => {
      const data: CellResult = JSON.parse(e.data)
      setResults(prev => [...prev, data])
      setProgress(prev => ({ ...prev, completed: prev.completed + 1 }))
    })

    es.addEventListener('done', () => {
      setRunningId(null)
      es.close()
      loadHistory()
    })

    es.onerror = () => {
      es.close()
      setRunningId(null)
    }
  }

  const cancelTest = async () => {
    if (!runningId) return
    try {
      await adminCancelPerfTest(runningId)
      eventSourceRef.current?.close()
      setRunningId(null)
    } catch { /* ignore */ }
  }

  const expandRun = async (id: number) => {
    if (expandedRun === id) {
      setExpandedRun(null)
      return
    }
    try {
      const res = await adminGetPerfTestRun(id)
      setExpandedResults(res.data.results || [])
      setExpandedRun(id)
    } catch { /* ignore */ }
  }

  const totalCells = selectedChannels.length * parseNumberList(inputSizesText).length * parseNumberList(outputSizesText).length

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <h1 className="text-xl font-semibold text-gray-900 mb-6">性能测试</h1>

      {/* Tabs */}
      <div className="flex gap-1 mb-6 bg-gray-100 rounded-lg p-1 w-fit">
        {(['config', 'running', 'history'] as const).map(t => (
          <button
            key={t}
            onClick={() => setTab(t)}
            className={`px-4 py-1.5 rounded-md text-sm font-medium transition-all ${
              tab === t ? 'bg-white text-gray-900 shadow-sm' : 'text-gray-500 hover:text-gray-700'
            }`}
          >
            {t === 'config' ? '配置测试' : t === 'running' ? '运行中' : '历史记录'}
          </button>
        ))}
      </div>

      {/* Config Tab */}
      {tab === 'config' && (
        <div className="space-y-6">
          {/* Channel Selection */}
          <div className="bg-white rounded-lg border border-gray-200 p-5">
            <h3 className="text-sm font-medium text-gray-700 mb-3">选择渠道</h3>
            <div className="space-y-3">
              {channelOptions.map(opt => {
                const selected = selectedChannels.find(c => c.name === opt.name)
                return (
                  <div key={opt.name} className="flex items-center gap-3 flex-wrap">
                    <label className="flex items-center gap-2 w-40 flex-shrink-0">
                      <input
                        type="checkbox"
                        checked={!!selected}
                        onChange={() => toggleChannel(opt.name)}
                        className="rounded border-gray-300 text-red-600 focus:ring-red-500"
                      />
                      <span className="text-sm text-gray-700">{opt.label}</span>
                    </label>
                    {selected && (
                      <div className="flex items-center gap-2 flex-wrap">
                        {/* Backend selector (only for backend channel) */}
                        {opt.backends && opt.backends.length > 0 && (
                          <select
                            value={selected.backend_name || ''}
                            onChange={e => updateChannelBackend(opt.name, e.target.value)}
                            className="px-2 py-1.5 text-sm border border-gray-200 rounded-md focus:outline-none focus:ring-1 focus:ring-red-500"
                          >
                            {opt.backends.map(b => (
                              <option key={b} value={b}>{b}</option>
                            ))}
                          </select>
                        )}
                        {/* Model selector */}
                        {opt.models && opt.models.length > 0 ? (
                          <select
                            value={selected.model}
                            onChange={e => updateChannelModel(opt.name, e.target.value)}
                            className="px-2 py-1.5 text-sm border border-gray-200 rounded-md focus:outline-none focus:ring-1 focus:ring-red-500 max-w-xs"
                          >
                            {opt.models.map(m => (
                              <option key={m} value={m}>{m}</option>
                            ))}
                          </select>
                        ) : (
                          <input
                            type="text"
                            value={selected.model}
                            onChange={e => updateChannelModel(opt.name, e.target.value)}
                            placeholder="模型名称"
                            className="px-3 py-1.5 text-sm border border-gray-200 rounded-md focus:outline-none focus:ring-1 focus:ring-red-500 w-64"
                          />
                        )}
                      </div>
                    )}
                  </div>
                )
              })}
              {channelOptions.length === 0 && (
                <p className="text-sm text-gray-400">加载渠道配置中...</p>
              )}
            </div>
          </div>

          {/* Grid Configuration */}
          <div className="bg-white rounded-lg border border-gray-200 p-5">
            <h3 className="text-sm font-medium text-gray-700 mb-3">测试网格</h3>
            <div className="grid grid-cols-2 gap-4">
              <div>
                <label className="block text-xs text-gray-500 mb-1">Input Token 数量（逗号分隔）</label>
                <input
                  type="text"
                  value={inputSizesText}
                  onChange={e => setInputSizesText(e.target.value)}
                  className="w-full px-3 py-1.5 text-sm border border-gray-200 rounded-md focus:outline-none focus:ring-1 focus:ring-red-500"
                />
              </div>
              <div>
                <label className="block text-xs text-gray-500 mb-1">Output Token 数量 / max_tokens（逗号分隔）</label>
                <input
                  type="text"
                  value={outputSizesText}
                  onChange={e => setOutputSizesText(e.target.value)}
                  className="w-full px-3 py-1.5 text-sm border border-gray-200 rounded-md focus:outline-none focus:ring-1 focus:ring-red-500"
                />
              </div>
            </div>
            <p className="mt-3 text-xs text-gray-400">
              总计 {totalCells} 个测试单元 ({selectedChannels.length} 渠道 × {parseNumberList(inputSizesText).length} input × {parseNumberList(outputSizesText).length} output)
            </p>
          </div>

          {/* Start Button */}
          <button
            onClick={startTest}
            disabled={selectedChannels.length === 0 || totalCells === 0 || !!runningId}
            className="px-5 py-2 bg-red-600 text-white text-sm font-medium rounded-lg hover:bg-red-700 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
          >
            开始测试
          </button>
        </div>
      )}

      {/* Running Tab */}
      {tab === 'running' && (
        <div className="space-y-4">
          {/* Progress Bar */}
          <div className="bg-white rounded-lg border border-gray-200 p-5">
            <div className="flex items-center justify-between mb-2">
              <span className="text-sm text-gray-700">
                {runningId ? `测试进行中: ${progress.completed}/${progress.total}` : '无运行中的测试'}
              </span>
              {runningId && (
                <button
                  onClick={cancelTest}
                  className="text-xs text-red-600 hover:text-red-700 font-medium"
                >
                  取消
                </button>
              )}
            </div>
            {progress.total > 0 && (
              <>
                <div className="w-full bg-gray-100 rounded-full h-2">
                  <div
                    className="bg-red-600 h-2 rounded-full transition-all duration-300"
                    style={{ width: `${(progress.completed / progress.total) * 100}%` }}
                  />
                </div>
                {runningId && progress.channel && (
                  <p className="mt-2 text-xs text-gray-400">
                    当前: {progress.channel} / input={progress.input} / output={progress.output}
                  </p>
                )}
              </>
            )}
          </div>

          {/* Results Grid */}
          {results.length > 0 && <ResultsGrid results={results} inputSizes={inputSizes} outputSizes={outputSizes} />}
        </div>
      )}

      {/* History Tab */}
      {tab === 'history' && (
        <div className="space-y-3">
          {history.length === 0 && (
            <p className="text-sm text-gray-400">暂无历史记录</p>
          )}
          {history.map(run => (
            <div key={run.id} className="bg-white rounded-lg border border-gray-200">
              <div
                className="p-4 flex items-center justify-between cursor-pointer hover:bg-gray-50"
                onClick={() => expandRun(run.id)}
              >
                <div className="flex items-center gap-4">
                  <span className={`inline-block px-2 py-0.5 rounded text-xs font-medium ${
                    run.status === 'completed' ? 'bg-green-50 text-green-700' :
                    run.status === 'running' ? 'bg-blue-50 text-blue-700' :
                    run.status === 'cancelled' ? 'bg-yellow-50 text-yellow-700' :
                    'bg-gray-100 text-gray-600'
                  }`}>
                    {run.status}
                  </span>
                  <span className="text-sm text-gray-700">
                    {new Date(run.created_at).toLocaleString()}
                  </span>
                  <span className="text-xs text-gray-400">
                    {run.completed_cells}/{run.total_cells} cells
                  </span>
                </div>
                <span className="text-xs text-gray-400">{expandedRun === run.id ? '▲' : '▼'}</span>
              </div>
              {expandedRun === run.id && expandedResults.length > 0 && (
                <div className="border-t border-gray-100 p-4">
                  <ResultsGrid
                    results={expandedResults}
                    inputSizes={JSON.parse(run.input_sizes)}
                    outputSizes={JSON.parse(run.output_sizes)}
                  />
                </div>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

function ResultsGrid({ results, inputSizes, outputSizes }: {
  results: CellResult[]
  inputSizes: number[]
  outputSizes: number[]
}) {
  const channels = [...new Set(results.map(r => r.channel))]

  const getResult = (channel: string, input: number, output: number) =>
    results.find(r => r.channel === channel && r.input_tokens === input && r.max_tokens === output)

  const getMetricColor = (ttft: number) => {
    if (ttft < 500) return 'text-green-700 bg-green-50'
    if (ttft < 1500) return 'text-yellow-700 bg-yellow-50'
    return 'text-red-700 bg-red-50'
  }

  return (
    <div className="overflow-x-auto">
      <table className="w-full text-xs">
        <thead>
          <tr className="border-b border-gray-200">
            <th className="text-left p-2 text-gray-500 font-medium">渠道</th>
            {inputSizes.map(input =>
              outputSizes.map(output => (
                <th key={`${input}-${output}`} className="p-2 text-center text-gray-500 font-medium">
                  <div>in={input}</div>
                  <div>out={output}</div>
                </th>
              ))
            )}
          </tr>
        </thead>
        <tbody>
          {channels.map(channel => (
            <tr key={channel} className="border-b border-gray-100">
              <td className="p-2 font-medium text-gray-700 whitespace-nowrap">{channel}</td>
              {inputSizes.map(input =>
                outputSizes.map(output => {
                  const r = getResult(channel, input, output)
                  if (!r) return <td key={`${input}-${output}`} className="p-2 text-center text-gray-300">—</td>
                  if (r.status === 'error') return (
                    <td key={`${input}-${output}`} className="p-2 text-center">
                      <div className="bg-red-50 rounded px-1.5 py-1 inline-block max-w-[140px]">
                        <div className="text-red-600 font-medium">ERROR</div>
                        <div className="text-[10px] text-red-400 truncate" title={r.error_msg}>
                          {r.error_msg || 'unknown'}
                        </div>
                      </div>
                    </td>
                  )
                  return (
                    <td key={`${input}-${output}`} className="p-2 text-center">
                      <div className={`rounded px-1.5 py-0.5 inline-block ${getMetricColor(r.ttft_ms)}`}>
                        <div className="font-mono font-medium">{r.ttft_ms.toFixed(0)}ms</div>
                        <div className="text-[10px] opacity-75">{r.tokens_per_second.toFixed(1)} tok/s</div>
                      </div>
                    </td>
                  )
                })
              )}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
