import type { QueuedAction, Session, TaskSummary } from '../api'
import { app, queuedFor, reasonsOf, sessionsOf } from './app.svelte'
import { outsideScope } from './scope.svelte'

/** One decision for the person. Work only an agent can do (failing checks, an idle agent) is not here. */
export type InboxKind = 'restack' | 'scope' | 'plan' | 'permission' | 'question' | 'review' | 'deliver' | 'push' | 'pr' | 'queued' | 'config'

export interface InboxItem {
  key: string
  kind: InboxKind
  projectId: string
  since: string
  task?: TaskSummary
  session?: Session
  queued?: QueuedAction
  /** The agent also waits for an answer on this task, behind the main decision. */
  also?: 'permission' | 'question'
}

function kindOf(task: TaskSummary): InboxKind | null {
  if (task.stackError) return 'restack'
  if (outsideScope[`${task.projectId}/${task.id}`]) return 'scope'
  if (queuedFor(task.projectId, task.id).length) return 'queued'
  const reasons = reasonsOf(task)
  if (reasons.includes('approvePlan')) return 'plan'
  const ss = sessionsOf(task.projectId, task.id)
  if (ss.some((s) => s.status === 'waiting' && s.waitingFor === 'permission')) return 'permission'
  if (task.hasOpenQuestion || ss.some((s) => s.status === 'waiting' && (s.waitingFor === 'input' || s.waitingFor === 'trust'))) return 'question'
  if (reasons.includes('review')) return 'review'
  if (reasons.includes('deliver')) return 'deliver'
  if (reasons.includes('push')) return task.pushed ? 'pr' : 'push'
  return null
}

const ORDER: InboxKind[] = ['plan', 'permission', 'question', 'restack', 'scope', 'review', 'deliver', 'push', 'pr', 'config', 'queued']

export const inboxItems = (): InboxItem[] => {
  const out: InboxItem[] = []
  for (const task of app.data?.tasks ?? []) {
    if (task.readOnly || task.merged) continue
    const kind = kindOf(task)
    if (!kind) continue
    const q = queuedFor(task.projectId, task.id)[0]
    const ss = sessionsOf(task.projectId, task.id)
    const asks: InboxItem['also'] = ss.some((s) => s.status === 'waiting' && s.waitingFor === 'permission')
      ? 'permission'
      : task.hasOpenQuestion || ss.some((s) => s.status === 'waiting' && (s.waitingFor === 'input' || s.waitingFor === 'trust'))
        ? 'question'
        : undefined
    out.push({ key: `${task.projectId}/${task.id}`, kind, projectId: task.projectId, since: q?.time ?? task.updatedAt, task, queued: q, also: asks && asks !== kind ? asks : undefined })
  }
  for (const s of app.data?.sessions ?? []) {
    if (s.taskId || s.status !== 'waiting') continue
    out.push({ key: s.id, kind: s.waitingFor === 'permission' ? 'permission' : 'question', projectId: s.projectId, since: s.startedAt, session: s })
  }
  for (const q of app.data?.queued ?? []) {
    if (q.kind !== 'config' && q.kind !== 'commit-config') continue
    out.push({ key: q.id, kind: 'config', projectId: q.projectId, since: q.time, queued: q })
  }
  // Decisions that unblock work first, then the newest.
  return out.sort((a, b) => ORDER.indexOf(a.kind) - ORDER.indexOf(b.kind) || b.since.localeCompare(a.since))
}

export const agentsWorking = () => (app.data?.sessions ?? []).some((s) => s.status === 'starting' || s.status === 'working')
