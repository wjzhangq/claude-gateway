import { useState, useEffect, useCallback } from 'react'
import { PieChart, Pie, Cell, ResponsiveContainer, Tooltip, Legend } from 'recharts'
import { adminGetAttribution, type AttributionResponse, type AttrGroup, type AttrSide } from '../api'

function formatTokens(n: number): string {
  if (!n) return '0'
  if (n >= 1_000_000_000) return (n / 1_000_000_000).toFixed(2) + 'B'
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(0) + 'K'
  return n.toString()
}

function formatCost(n: number): string {
  return '$' + n.toLocaleString('en-US', { maximumFractionDigits: 0 })
}

const DAYS_OPTIONS = [
  { value: 0, label: '全量' },
  { value: 30, label: '近 30 天' },
  { value: 7, label: '近 7 天' },
]

const SHEN_COLOR = '#dc2626' // red-600
const NON_COLOR = '#0ea5e9'  // sky-500 — high contrast against the red side
// Group palette: distinct hues so adjacent pie slices are easy to tell apart.
// Red stays first to keep the admin theme, then spread across the color wheel.
const GROUP_PALETTE = [
  '#dc2626', // red
  '#0ea5e9', // sky
  '#f59e0b', // amber
  '#16a34a', // green
  '#8b5cf6', // violet
  '#ec4899', // pink
  '#14b8a6', // teal
  '#f97316', // orange
  '#6366f1', // indigo
  '#84cc16', // lime
]

/** Cost-ratio band color: hot red at 100%, fading to amber as the share drops. */
function costRatioColor(ratio: number): string {
  const r = Math.max(0, Math.min(1, ratio))
  const hue = 6 + (1 - r) * 46 // 6° (red) at 100% → 52° (amber) at low share
  return `hsl(${hue}, 88%, 52%)`
}

/** Two-team token share donut. */
function TeamPie({ data }: { data: AttributionResponse }) {
  const pieData = [
    { name: data.shen.label, value: data.shen.tokens },
    { name: data.non.label, value: data.non.tokens },
  ]
  const total = data.shen.tokens + data.non.tokens
  return (
    <ResponsiveContainer width="100%" height={220}>
      <PieChart>
        <Pie
          data={pieData}
          dataKey="value"
          nameKey="name"
          cx="50%"
          cy="50%"
          innerRadius={55}
          outerRadius={85}
          paddingAngle={2}
        >
          <Cell fill={SHEN_COLOR} />
          <Cell fill={NON_COLOR} />
        </Pie>
        <Tooltip
          formatter={(v) => {
            const n = Number(v) || 0
            return [`${formatTokens(n)} (${total ? ((n / total) * 100).toFixed(1) : 0}%)`, 'Tokens']
          }}
        />
        <Legend verticalAlign="bottom" height={24} />
      </PieChart>
    </ResponsiveContainer>
  )
}

/** Palette color for a leader, keyed by its rank in the side (1-based). */
function leaderColor(rank: number): string {
  return GROUP_PALETTE[(rank - 1) % GROUP_PALETTE.length]
}

/** Per-leader-group token share donut inside one side, with a color legend. */
function LeaderPie({ side }: { side: AttrSide }) {
  // Color by full-list rank so a leader's slice matches its table row swatch.
  const active = side.groups
    .map((g, i) => ({ name: g.leader, value: g.tokens, color: leaderColor(i + 1) }))
    .filter(d => d.value > 0)
  const total = active.reduce((s, d) => s + d.value, 0) || 1
  return (
    <>
      <ResponsiveContainer width="100%" height={220}>
        <PieChart>
          <Pie data={active} dataKey="value" nameKey="name" cx="50%" cy="50%" innerRadius={50} outerRadius={80}>
            {active.map((d, i) => (
              <Cell key={i} fill={d.color} />
            ))}
          </Pie>
          <Tooltip
            formatter={(v) => {
              const n = Number(v) || 0
              return [`${formatTokens(n)} (${((n / total) * 100).toFixed(1)}%)`, 'Tokens']
            }}
          />
        </PieChart>
      </ResponsiveContainer>
      <ul className="mt-2 space-y-1 max-h-40 overflow-auto">
        {active.map((d, i) => (
          <li key={i} className="flex items-center gap-1.5 text-xs">
            <span className="inline-block w-2.5 h-2.5 rounded-sm shrink-0" style={{ backgroundColor: d.color }} />
            <span className="text-gray-600 truncate flex-1">{d.name}</span>
            <span className="text-gray-400 tabular-nums">{((d.value / total) * 100).toFixed(1)}%</span>
          </li>
        ))}
      </ul>
    </>
  )
}

/** One expandable leader-group row (click to reveal members). */
function GroupRow({ group, rank }: { group: AttrGroup; rank: number }) {
  const [open, setOpen] = useState(false)
  const maxCost = group.members.reduce((m, x) => Math.max(m, x.cost), 0)
  return (
    <>
      <tr onClick={() => setOpen(o => !o)} className="cursor-pointer hover:bg-gray-50/60">
        <td className="px-4 py-2.5 text-gray-400 text-xs">{rank}</td>
        <td className="px-4 py-2.5">
          <span className="mr-1.5 text-gray-400 text-[11px]">{open ? '▼' : '▶'}</span>
          <span className="inline-block w-2.5 h-2.5 rounded-sm mr-1.5 align-middle" style={{ backgroundColor: leaderColor(rank) }} />
          <span className="font-medium text-gray-800">{group.leader}</span>
        </td>
        <td className="px-4 py-2.5 text-right text-gray-600">{group.active_count} / {group.org_count}</td>
        <td className="px-4 py-2.5 text-right font-semibold">{formatTokens(group.tokens)}</td>
        <td className="px-4 py-2.5 text-right text-red-600">{formatCost(group.cost)}</td>
        <td className="px-4 py-2.5 text-right text-gray-600">{group.requests.toLocaleString()}</td>
      </tr>
      {open && group.members.map(m => (
        <tr key={m.itcode} className="bg-gray-50/40">
          <td></td>
          <td className="pl-9 pr-4 py-1.5 text-xs">
            <span className={m.tokens > 0 ? 'text-gray-700' : 'text-gray-400'}>{m.name || m.itcode}</span>
            <span className="text-gray-400 ml-1.5">{m.itcode}</span>
          </td>
          <td></td>
          <td className={`px-4 py-1.5 text-right text-xs ${m.tokens > 0 ? 'text-gray-700' : 'text-gray-400'}`}>
            {formatTokens(m.tokens)}
          </td>
          <td className="px-4 py-1.5 text-xs">
            {m.cost > 0 ? (
              <div className="flex items-center justify-end gap-2">
                <div className="flex-1 h-2 rounded-full bg-gray-100 overflow-hidden max-w-[90px]">
                  <div
                    className="h-full rounded-full"
                    style={{
                      width: `${maxCost ? (m.cost / maxCost) * 100 : 0}%`,
                      backgroundColor: costRatioColor(maxCost ? m.cost / maxCost : 0),
                    }}
                  />
                </div>
                <span className="text-gray-600 tabular-nums w-12 text-right">{formatCost(m.cost)}</span>
              </div>
            ) : (
              <div className="text-right text-gray-400">{formatCost(m.cost)}</div>
            )}
          </td>
          <td className="px-4 py-1.5 text-right text-xs text-gray-400">{m.requests.toLocaleString()}</td>
        </tr>
      ))}
    </>
  )
}

/** One team block: header band + left table + right pie. */
function SideBlock({ side }: { side: AttrSide }) {
  return (
    <div className="bg-white rounded-xl shadow-sm overflow-hidden mb-5">
      <div className="flex items-center gap-4 flex-wrap px-5 py-3.5 border-l-4 border-red-600 bg-red-50/40 border-b border-gray-100">
        <h2 className="text-lg font-bold text-red-700 m-0">{side.label}</h2>
        <span className="text-sm text-gray-500">
          {side.group_count} 组 · 有消耗 <b className="text-gray-700">{side.active_count}</b> / 组织 <b className="text-gray-700">{side.org_count}</b> 人
        </span>
        <span className="ml-auto text-sm">
          <b className="text-red-700 text-base">{formatTokens(side.tokens)}</b>
          <span className="text-gray-400"> tokens · </span>
          <b className="text-red-600">{formatCost(side.cost)}</b>
        </span>
      </div>
      <div className="grid grid-cols-[minmax(0,1fr)_280px] gap-5 items-start p-5 max-lg:grid-cols-1">
        <table className="w-full text-sm">
          <thead>
            <tr className="bg-gray-50 text-gray-500 text-xs uppercase tracking-wide text-left">
              <th className="px-4 py-2.5 w-10">#</th>
              <th className="px-4 py-2.5">负责人</th>
              <th className="px-4 py-2.5 text-right">有消耗/组织</th>
              <th className="px-4 py-2.5 text-right">Token</th>
              <th className="px-4 py-2.5 text-right">费用</th>
              <th className="px-4 py-2.5 text-right">请求</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-50">
            {side.groups.map((g, i) => (
              <GroupRow key={`${g.side}-${g.leader}`} group={g} rank={i + 1} />
            ))}
          </tbody>
        </table>
        <div className="bg-gray-50/60 rounded-lg p-3">
          <div className="text-xs text-gray-400 text-center mb-1">各负责人组占比</div>
          <LeaderPie side={side} />
        </div>
      </div>
    </div>
  )
}

export default function AdminInsightAttributionPage() {
  const [data, setData] = useState<AttributionResponse | null>(null)
  const [loading, setLoading] = useState(true)
  const [days, setDays] = useState(0)
  const [error, setError] = useState<string | null>(null)
  const [activeTab, setActiveTab] = useState<'shen' | 'non'>('shen')

  const load = useCallback(async (d: number) => {
    setError(null)
    try {
      const { data } = await adminGetAttribution(d)
      setData(data)
    } catch (e: any) {
      setError(e?.response?.data?.error || e?.message || '加载失败')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { load(days) }, [days, load])

  if (loading) {
    return <div className="p-6 max-w-[1200px] mx-auto"><div className="p-10 text-center text-gray-400">加载中…</div></div>
  }
  if (error && !data) {
    return <div className="p-6 max-w-[1200px] mx-auto"><div className="p-10 text-center text-red-600">⚠ {error}</div></div>
  }
  if (!data) return null

  const grandTokens = data.shen.tokens + data.non.tokens
  const grandCost = data.shen.cost + data.non.cost

  return (
    <div className="p-6 max-w-[1200px] mx-auto">
      <h1 className="text-xl font-semibold text-gray-900 mb-1">Token 归口</h1>
      <p className="text-sm text-gray-400 mb-4">
        按组织负责人组聚合的 {data.shen.label} / {data.non.label} Token 消耗 · {data.meta.period}
      </p>

      {/* Controls */}
      <div className="flex gap-2 items-center mb-4 flex-wrap">
        {DAYS_OPTIONS.map(o => (
          <button
            key={o.value}
            onClick={() => setDays(o.value)}
            className={`px-3 py-1.5 rounded-lg text-sm font-medium ${
              days === o.value ? 'bg-red-600 text-white' : 'bg-white text-gray-600 border border-gray-200 hover:bg-gray-50'
            }`}
          >{o.label}</button>
        ))}
        {error && <span className="text-xs text-red-600 ml-2">⚠ {error}</span>}
      </div>

      {/* Overview card: metrics grid + team share pie */}
      <div className="bg-white rounded-xl shadow-sm overflow-hidden mb-5 grid grid-cols-[minmax(0,1fr)_320px] gap-0 items-stretch max-lg:grid-cols-1">
        <div className="grid grid-cols-2 grid-rows-2 gap-px bg-gray-100">
          {[
            { val: formatTokens(data.shen.tokens), label: `${data.shen.label} Tokens`, c: 'text-red-700' },
            { val: formatTokens(data.non.tokens), label: `${data.non.label} Tokens`, c: 'text-orange-600' },
            { val: formatTokens(grandTokens), label: '合计 Tokens', c: 'text-gray-800' },
            { val: formatCost(grandCost), label: '合计费用', c: 'text-red-600' },
          ].map((m, i) => (
            <div key={i} className="bg-white px-4 py-5 text-center flex flex-col justify-center">
              <div className={`text-2xl font-bold ${m.c}`}>{m.val}</div>
              <div className="text-xs text-gray-400 mt-1">{m.label}</div>
            </div>
          ))}
        </div>
        <div className="border-l border-gray-100 p-3 max-lg:border-l-0 max-lg:border-t">
          <div className="text-xs text-gray-500 text-center font-medium mb-1">团队 Token 占比</div>
          <TeamPie data={data} />
        </div>
      </div>

      {/* Team tabs */}
      <div className="flex gap-2 mb-4">
        {[
          { key: 'shen' as const, label: data.shen.label, tokens: data.shen.tokens },
          { key: 'non' as const, label: data.non.label, tokens: data.non.tokens },
        ].map(t => {
          const on = activeTab === t.key
          return (
            <button
              key={t.key}
              onClick={() => setActiveTab(t.key)}
              className={`flex-1 px-4 py-3 text-left border-b-[3px] transition-colors ${
                on ? 'border-red-600' : 'border-gray-200 hover:border-gray-300'
              }`}
            >
              <div className={`text-sm font-bold ${on ? 'text-red-700' : 'text-gray-500'}`}>{t.label}</div>
              <div className="text-xs text-gray-400 mt-0.5">{formatTokens(t.tokens)} tokens</div>
            </button>
          )
        })}
      </div>

      {activeTab === 'shen' ? <SideBlock side={data.shen} /> : <SideBlock side={data.non} />}

      {/* Coverage diagnostics */}
      {data.unmatched_total > 0 && (
        <div className="px-4 py-3 bg-amber-50 border border-amber-200 rounded-lg mb-3 text-sm text-amber-800">
          ⚠ 有 <b>{data.unmatched_total}</b> 个有消耗账号未归口（新入职/未纳入快照），确认归属后重跑 orgimport 刷新：
          {data.unmatched.slice(0, 10).map(u => (
            <span key={u.itcode} className="ml-2">{u.itcode}（{formatTokens(u.tokens)}）</span>
          ))}
        </div>
      )}
      {data.departed_total > 0 && (
        <div className="px-4 py-3 bg-gray-50 border border-gray-200 rounded-lg text-sm text-gray-500">
          🗂 离职人员 <b>{data.departed_total}</b> 人（历史消耗 {formatTokens(data.departed_tokens)} · {formatCost(data.departed_cost)}），不计入团队汇总。
        </div>
      )}
    </div>
  )
}
