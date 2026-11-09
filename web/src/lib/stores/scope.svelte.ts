import { api, type TaskSummary } from '../api'
import { limited } from '../limit'

/** Files outside each task's scope ("pid/id" → n), for the inbox: the snapshot does not carry it. */
export const outsideScope = $state<Record<string, number>>({})

const fetched = new Map<string, { updated: string; at: number }>()
const STALE_MS = 60_000

// Any task with work on its branch can have files outside its scope, delivered ones included.
const WITH_WORK = new Set(['building', 'verified', 'reviewed', 'delivered'])
export const scopeMatters = (x: TaskSummary) => !x.readOnly && !x.merged && WITH_WORK.has(x.state)

export function trackScopes(tasks: TaskSummary[]) {
  const now = Date.now()
  for (const x of tasks) {
    if (!scopeMatters(x)) continue
    const key = `${x.projectId}/${x.id}`
    const last = fetched.get(key)
    if (last && last.updated === x.updatedAt && now - last.at < STALE_MS) continue
    fetched.set(key, { updated: x.updatedAt, at: now })
    limited(() => api.scopePreview(x.projectId, x.id))
      .then((r) => (outsideScope[key] = Math.max(r.outsideCount ?? 0, r.outside.length)))
      .catch(() => (outsideScope[key] = 0))
  }
}
