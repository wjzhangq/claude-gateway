const TZ = 'Asia/Shanghai'

/** 返回服务器本地日期字符串 YYYY-MM-DD，用于 API 查询 */
export function toDateStr(d: Date): string {
  const parts = d.toLocaleDateString('sv-SE', { timeZone: TZ })
  return parts // sv-SE locale outputs YYYY-MM-DD
}

/** 格式化为服务器本地时间，如 2024-01-15 10:30:00 */
export function formatTime(s: string | null | undefined): string {
  if (!s) return '—'
  const d = new Date(s)
  if (isNaN(d.getTime())) return s
  return d.toLocaleString('zh-CN', {
    timeZone: TZ,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  })
}

/** 格式化为服务器本地日期，如 2024-01-15 */
export function formatDate(s: string | null | undefined): string {
  if (!s) return '—'
  const d = new Date(s)
  if (isNaN(d.getTime())) return s
  return d.toLocaleString('zh-CN', {
    timeZone: TZ,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
  })
}
