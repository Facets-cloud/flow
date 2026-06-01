// Small, dependency-free formatting helpers. Durations read like an operator
// log ("3m", "2h", "4d"), never raw seconds dumps.

export function fromSeconds(sec: number): string {
  if (sec < 0) sec = 0
  if (sec < 60) return `${sec}s`
  if (sec < 3600) return `${Math.floor(sec / 60)}m`
  if (sec < 86400) return `${Math.floor(sec / 3600)}h`
  return `${Math.floor(sec / 86400)}d`
}

export function fromMinutes(min: number): string {
  if (min < 0) min = 0
  if (min < 1) return 'just now'
  if (min < 60) return `${min}m`
  if (min < 1440) return `${Math.floor(min / 60)}h`
  return `${Math.floor(min / 1440)}d`
}

export function ago(iso: string | null | undefined): string {
  if (!iso) return '—'
  const t = Date.parse(iso)
  if (Number.isNaN(t)) return '—'
  const sec = Math.max(0, Math.floor((Date.now() - t) / 1000))
  return sec < 45 ? 'just now' : `${fromSeconds(sec)} ago`
}

export function shortDate(iso: string | null | undefined): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
}

export function dateTime(iso: string | null | undefined): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  return d.toLocaleString(undefined, {
    month: 'short',
    day: 'numeric',
    hour: 'numeric',
    minute: '2-digit',
  })
}

export function pct(used: number, max: number): number {
  if (!max) return 0
  return Math.min(100, Math.round((used / max) * 100))
}

export function compact(n: number): string {
  if (n < 1000) return String(n)
  if (n < 1_000_000) return `${(n / 1000).toFixed(n < 10000 ? 1 : 0)}k`
  if (n < 1_000_000_000) return `${(n / 1_000_000).toFixed(n < 10_000_000 ? 1 : 0)}M`
  return `${(n / 1_000_000_000).toFixed(2)}B`
}

// compactTokens is like compact but ALWAYS keeps 2 decimals, so token figures
// read as e.g. "12.34M" / "248.10k" instead of a rounded "12M". The decimals
// make live growth visible — with the 5s refetch you can watch the number tick
// instead of it looking frozen on a rounded value.
export function compactTokens(n: number): string {
  if (n < 1000) return String(n)
  if (n < 1_000_000) return `${(n / 1000).toFixed(2)}k`
  if (n < 1_000_000_000) return `${(n / 1_000_000).toFixed(2)}M`
  return `${(n / 1_000_000_000).toFixed(2)}B`
}

export function titleCase(s: string): string {
  return s.replace(/[-_]/g, ' ').replace(/\b\w/g, (c) => c.toUpperCase())
}

// Local YYYY-MM-DD for "today". Due dates are YYYY-MM-DD, so plain string
// comparison against this is chronological (and treats the date as local, not
// UTC midnight).
export function todayISO(): string {
  const n = new Date()
  return `${n.getFullYear()}-${String(n.getMonth() + 1).padStart(2, '0')}-${String(n.getDate()).padStart(2, '0')}`
}

// Badge tone for a due-date pill: danger when overdue, warn when due
// today/tomorrow, neutral otherwise. Takes primitives so both the Tasks table
// and the Overview backlog rows can share it.
export function dueTone(dueDate: string | null, dueInfo: string | null): '' | 'warn' | 'danger' {
  if (!dueDate) return ''
  if (dueDate < todayISO()) return 'danger'
  if (dueInfo === 'today' || dueInfo === 'tomorrow') return 'warn'
  return ''
}

export const PROVIDER_LABEL: Record<string, string> = {
  claude: 'Claude Code',
  codex: 'Codex',
}
