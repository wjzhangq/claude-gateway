// Shared budget-color thresholds for the backend daily-limit display.
// Single source of truth so the fleet summary bar and per-row badges always
// produce the same color for the same percentage (FR-013, SC-003).

export type BudgetTier = 'healthy' | 'warn' | 'critical' | 'over' | 'none'

// budgetColor maps a usage percentage to a tier.
//   healthy  < 70
//   warn     70 – <90
//   critical 90 – 100
//   over     > 100
//   none     no positive cap (unlimited) — caller renders a neutral chip
export function budgetColor(pct: number | null | undefined, hasLimit: boolean): BudgetTier {
  if (!hasLimit || pct == null) return 'none'
  if (pct > 100) return 'over'
  if (pct >= 90) return 'critical'
  if (pct >= 70) return 'warn'
  return 'healthy'
}

// Tailwind classes per tier for a solid fill (progress bars, badge backgrounds).
export const TIER_FILL: Record<BudgetTier, string> = {
  healthy: 'bg-green-500',
  warn: 'bg-amber-500',
  critical: 'bg-red-500',
  over: 'bg-red-700',
  none: 'bg-gray-300',
}

// Tailwind classes per tier for a soft badge (light background + text + ring).
export const TIER_BADGE: Record<BudgetTier, string> = {
  healthy: 'bg-green-50 text-green-700 ring-green-100',
  warn: 'bg-amber-50 text-amber-700 ring-amber-100',
  critical: 'bg-red-50 text-red-700 ring-red-100',
  over: 'bg-red-100 text-red-800 ring-red-200',
  none: 'bg-gray-50 text-gray-500 ring-gray-100',
}
