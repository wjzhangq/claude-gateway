import { useSearchParams } from 'react-router-dom'
import AdminInsightRankingPage from './AdminInsightRankingPage'
import AdminInsightUserPage from './AdminInsightUserPage'
import AdminInsightAttributionPage from './AdminInsightAttributionPage'
import AdminInsightOrgPage from './AdminInsightOrgPage'

type Tab = 'ranking' | 'user' | 'attribution' | 'org'

const TABS: { key: Tab; label: string }[] = [
  { key: 'attribution', label: 'Token 归口' },
  { key: 'ranking', label: '用量排名' },
  { key: 'user', label: '用户洞察' },
  { key: 'org', label: '组织管理' },
]

function isTab(v: string | null): v is Tab {
  return v === 'ranking' || v === 'user' || v === 'attribution' || v === 'org'
}

// AdminInsightPage merges the four Insight views into a single tabbed page.
// The active tab and the selected user id live in the query string (?tab=, ?uid=)
// so a view is shareable and survives refresh.
export default function AdminInsightPage() {
  const [params, setParams] = useSearchParams()
  const tabParam = params.get('tab')
  const tab: Tab = isTab(tabParam) ? tabParam : 'attribution'
  const uidParam = params.get('uid')
  const uid = uidParam ? Number(uidParam) : undefined

  const setTab = (t: Tab) => {
    const next = new URLSearchParams(params)
    next.set('tab', t)
    if (t !== 'user') next.delete('uid')
    setParams(next, { replace: true })
  }

  // Jump to the user tab with a specific user selected (from the ranking table).
  const selectUser = (userId: number | null) => {
    const next = new URLSearchParams(params)
    next.set('tab', 'user')
    if (userId == null) next.delete('uid')
    else next.set('uid', String(userId))
    setParams(next, { replace: true })
  }

  return (
    <div>
      <div className="bg-white border-b border-gray-100 px-6 sticky top-0 z-10">
        <div className="max-w-[1200px] mx-auto flex gap-1">
          {TABS.map(t => {
            const on = tab === t.key
            return (
              <button
                key={t.key}
                onClick={() => setTab(t.key)}
                className={`px-4 py-3.5 text-sm font-medium border-b-2 transition-colors ${
                  on ? 'border-red-600 text-red-700' : 'border-transparent text-gray-500 hover:text-gray-800'
                }`}
              >
                {t.label}
              </button>
            )
          })}
        </div>
      </div>

      {tab === 'ranking' && <AdminInsightRankingPage onSelectUser={selectUser} />}
      {tab === 'user' && <AdminInsightUserPage userId={uid} onSelectUser={selectUser} />}
      {tab === 'attribution' && <AdminInsightAttributionPage />}
      {tab === 'org' && <AdminInsightOrgPage />}
    </div>
  )
}
