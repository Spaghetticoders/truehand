import { api, ApiError, type Machine, type QueuedAction, type Session, type SessionStatus, type SettingsPatch, type Snapshot, type TaskAction, type TaskSummary, type WaitReason } from '../api'
import { setLocale, t, type Key } from '../i18n/index.svelte'
import { shell } from '../api/http'

interface AppState {
  ready: boolean
  /** The browser is not paired yet: only pairing can be shown. */
  needsAuth: boolean
  error: string | null
  data: Snapshot | null
}

export const app = $state<AppState>({ ready: false, needsAuth: false, error: null, data: null })

let shellCanApprove: boolean | null = null
let relogged = false

async function relogin() {
  const core = shell()
  if (!core || relogged) return false
  relogged = true
  // A reload that lands on 401 again must not loop: after one retry, show pairing.
  try {
    const last = Number(sessionStorage.getItem('truehand.relogin') ?? 0)
    if (Date.now() - last < 30_000) return false
    sessionStorage.setItem('truehand.relogin', String(Date.now()))
  } catch {
    // no storage: the module flag still stops a second reload in this page
  }
  await core.invoke('start')
  location.reload()
  return true
}

let inflight: Promise<void> | null = null
let again = false

/** One snapshot at a time: calls made meanwhile collapse into a single follow-up. */
export function refresh(): Promise<void> {
  if (inflight) {
    again = true
    return inflight
  }
  inflight = load().finally(() => {
    inflight = null
    if (again) {
      again = false
      void refresh()
    }
  })
  return inflight
}

// Bumped by our own writes: a snapshot asked for before one of them is already old.
let writes = 0

async function load() {
  const started = Date.now()
  const seen = writes
  try {
    const snap = await api.snapshot()
    if (seen !== writes) return void (again = true)
    // A snapshot asked for before our own request was queued must not hide it.
    for (const n of noted) if (n.at >= started && !snap.queued.some((q) => q.id === n.q.id)) snap.queued.push(n.q)
    // The webview's own fetch has no human factor: inside the Mac app, ask through the shell.
    if (shellCanApprove === null) shellCanApprove = (await api.shellMe().catch(() => null))?.canApprove ?? false
    if (shellCanApprove) snap.me.canApprove = true
    app.data = snap
    app.needsAuth = false
    app.ready = true
    setLocale(snap.settings.language)
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) {
      // Inside the Mac app the webview is signed in by the shell: if the cookie got lost, it signs in again.
      if (await relogin()) return
      app.needsAuth = true
      app.ready = true
    } else app.error = String(e)
  }
}

let pending: ReturnType<typeof setTimeout> | undefined
const soon = () => {
  clearTimeout(pending)
  pending = setTimeout(() => void refresh(), 120)
}

export function startSync() {
  void refresh()
  return api.events((e) => {
    if (e.type === 'changed' && e.project === '') writes++
    if (e.type === 'session') {
      const s = app.data?.sessions.find((s) => s.id === e.id)
      if (s) {
        s.status = e.status
        s.waitingFor = e.waitingFor
      }
    } else if (e.type === 'machine') {
      const m = app.data?.machines?.find((m) => m.id === e.id)
      if (m) m.status = e.status
    }
    soon()
  })
}

export async function saveSettings(patch: SettingsPatch) {
  const s = await api.updateSettings(patch)
  if (app.data) app.data.settings = s
  setLocale(s.language)
  // Remote access starts after the reply: the snapshot says what really runs.
  await refresh()
}

export type Theme = 'system' | 'light' | 'dark'

const THEME_KEY = 'truehand.theme'

export const theme = $state<{ value: Theme }>({ value: readTheme() })

function readTheme(): Theme {
  try {
    const v = localStorage.getItem(THEME_KEY)
    return v === 'light' || v === 'dark' ? v : 'system'
  } catch {
    return 'system'
  }
}

export function applyTheme(v: Theme = theme.value) {
  theme.value = v
  const root = document.documentElement
  if (v === 'system') delete root.dataset.theme
  else root.dataset.theme = v
  try {
    localStorage.setItem(THEME_KEY, v)
  } catch {
    // private mode: the choice lasts for this page only
  }
}

export const project = (pid: string) => app.data?.projects.find((p) => p.id === pid)
export const clientName = (cid: string) => app.data?.clients.find((c) => c.id === cid)?.name || t('common.noClient')
export const taskOf = (pid: string, id: string) => app.data?.tasks.find((t) => t.projectId === pid && t.id === id)
export const sessionsOf = (pid: string, id: string) => app.data?.sessions.filter((s) => s.projectId === pid && s.taskId === id) ?? []
export const queuedFor = (pid: string, id: string) => app.data?.queued.filter((q) => q.projectId === pid && q.taskId === id) ?? []
export const machineOf = (pid: string): Machine | undefined => {
  const mid = project(pid)?.machineId ?? 'local'
  return app.data?.machines?.find((m) => m.id === mid)
}
export const isRemote = (pid: string) => (project(pid)?.machineId ?? 'local') !== 'local'
export const isOffline = (pid: string) => {
  const m = machineOf(pid)
  return !!m && m.id !== 'local' && m.status !== 'online'
}

export const isLive = (s: Session) => s.status !== 'exited'

export type Dot = 'working' | 'waiting' | 'idle' | 'error'

export function dotOf(s: Session): Dot {
  switch (s.status) {
    case 'starting':
    case 'working':
      return 'working'
    case 'waiting':
      return 'waiting'
    case 'exited':
      return crashed(s) ? 'error' : 'idle'
    default:
      return 'idle'
  }
}

export function dotOfAll(sessions: Session[]): Dot | null {
  if (!sessions.length) return null
  const dots = sessions.map(dotOf)
  for (const d of ['waiting', 'error', 'working'] as Dot[]) if (dots.includes(d)) return d
  return 'idle'
}

export const crashed = (s: Session) => s.status === 'exited' && !!s.exitCode && !s.stoppedByUser

export const statusKey = (s: Session): `status.${SessionStatus | 'crashed' | 'waitingPermission' | 'waitingTrust'}` =>
  crashed(s) ? 'status.crashed' : s.status === 'waiting' && s.waitingFor === 'permission' ? 'status.waitingPermission' : s.status === 'waiting' && s.waitingFor === 'trust' ? 'status.waitingTrust' : `status.${s.status}`

export type Bucket = 'approvals' | 'questions' | 'deliver' | 'blocked'

export const BUCKETS: Bucket[] = ['approvals', 'questions', 'deliver', 'blocked']

export const BUCKET_OF: Record<WaitReason | 'queued', Bucket> = {
  approvePlan: 'approvals',
  queued: 'approvals',
  question: 'questions',
  permission: 'questions',
  review: 'deliver',
  deliver: 'deliver',
  push: 'deliver',
  failing: 'blocked',
  stale: 'blocked',
}

/** Every reason a task waits on the human, so a row shows them all, not only the server's top one. */
export function reasonsOf(t: TaskSummary): WaitReason[] {
  const out: WaitReason[] = []
  const add = (r: WaitReason, cond: boolean) => cond && !out.includes(r) && out.push(r)
  add(t.waitReason ?? 'question', !!t.waitReason)
  const ss = sessionsOf(t.projectId, t.id)
  add('permission', ss.some((s) => s.waitingFor === 'permission'))
  add('question', t.hasOpenQuestion || ss.some((s) => s.waitingFor === 'input' || s.waitingFor === 'trust'))
  add('approvePlan', t.planReady && !queuedFor(t.projectId, t.id).length)
  add('failing', t.failing && !t.merged)
  add('stale', t.stale && !t.merged)
  return queuedFor(t.projectId, t.id).length ? out.filter((r) => r !== 'approvePlan' && r !== 'push') : out
}

/** The chip text: "to push" becomes "PR to open" once the branch is pushed. */
export const reasonKey = (task: TaskSummary | undefined, r: WaitReason | 'queued'): Key =>
  r === 'push' && task?.pushed ? 'wait.openPr' : `wait.${r}`

/** Shows a request queued for the Mac right away, before the next snapshot confirms it. */
const noted: { q: QueuedAction; at: number }[] = []

export function noteQueued(q: QueuedAction) {
  noted.push({ q, at: Date.now() })
  if (noted.length > 20) noted.shift()
  if (app.data && !app.data.queued.some((x) => x.id === q.id)) app.data.queued.push(q)
}

/** Actions that need the human factor: from a phone they are queued for the Mac. */
export const NEEDS_HUMAN: TaskAction[] = ['approve-plan', 'push', 'open-pr']
