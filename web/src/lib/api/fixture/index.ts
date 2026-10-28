import type {
  ActionArgs,
  ActResult,
  Activation,
  Api,
  Machine,
  NewTask,
  PermissionMode,
  Project,
  PtyHandlers,
  ServerEvent,
  Session,
  SettingsPatch,
  Snapshot,
  TaskAction,
  TaskDetail,
  TaskState,
  TaskSummary,
  WaitReason,
} from '../types'
import { TASK_STATES } from '../types'
import { ApiError } from '../http'
import { SPINNER, transcripts } from './transcripts'
import { activations, CONFIG_YAML, devices, machines, me, projects, queued, rows, sessions, settings, transcriptOf, clients, type Row } from './data'
import shotLogin from './shot-login.svg?url'
import shotReport from './shot-report.svg?url'
import shotToast from './shot-toast.svg?url'

const assets: Record<string, string> = { 'evidence/login.svg': shotLogin, 'evidence/report.svg': shotReport, 'evidence/toast.svg': shotToast }

const clone = <T>(v: T): T => structuredClone(v)
const now = () => new Date().toISOString()
const rid = (p: string) => `${p}${Math.random().toString(16).slice(2, 8)}`
// The fixture plays the phone at phone widths: no human factor there.
const canApprove = () => !window.matchMedia('(max-width: 700px)').matches
const live = (s: Session) => s.status !== 'exited'
const stripAnsi = (s: string) => s.replace(/\x1b\[[0-9;?]*[A-Za-z]/g, '')

for (const s of sessions) {
  const text = (transcripts[transcriptOf[s.id]] ?? []).map(([, t]) => t).join('')
  s.tail = stripAnsi(text)
    .split('\r\n')
    .map((l) => l.trimEnd())
    .filter((l) => l && !/^[╭│╰]/.test(l))
    .slice(-5)
}

const PRIORITY: WaitReason[] = ['permission', 'question', 'approvePlan', 'failing', 'stale', 'review', 'deliver', 'push']

/** The server's decide.go, so the mock shows what the server will. */
function decide(r: Row): { waitReason?: WaitReason; primaryAction?: TaskAction } {
  const p = projects.find((p) => p.id === r.projectId)
  if (!p?.active) return { primaryAction: 'activate' }
  if (r.branch && !r.worktree) return {}
  const ss = sessions.filter((s) => s.projectId === r.projectId && s.taskId === r.id)
  const liveDev = ss.some((s) => s.role === 'dev' && live(s))
  const liveReview = ss.some((s) => s.role === 'review' && live(s))
  const resumable = !liveDev && ss.some((s) => s.role === 'dev' && s.resumable)
  const held: Record<WaitReason, boolean> = {
    permission: ss.some((s) => live(s) && s.waitingFor === 'permission'),
    question: ss.some((s) => live(s) && s.waitingFor === 'input') || r.comments.some((c) => c.openQuestion),
    approvePlan: r.state === 'planned' && r.planReadyFact,
    failing: r.failing && !r.merged,
    stale: r.stale && !r.merged,
    review: r.state === 'verified' && !liveReview,
    deliver: r.state === 'reviewed' && r.deliverGateOpen,
    push: r.state === 'delivered' && !r.merged && !r.prUrl,
  }
  const waitReason = PRIORITY.find((w) => held[w])
  const agent = (): TaskAction => (liveDev ? 'open-agent' : resumable ? 'resume-agent' : 'start-agent')
  switch (waitReason) {
    case 'permission':
    case 'question':
      return { waitReason, primaryAction: 'open-agent' }
    case 'approvePlan':
      return { waitReason, primaryAction: 'approve-plan' }
    case 'stale':
      return { waitReason, primaryAction: 'send-back' }
    case 'review':
      return { waitReason, primaryAction: 'start-review' }
    case 'deliver':
      return { waitReason, primaryAction: 'deliver' }
    case 'push':
      return { waitReason, primaryAction: r.pushed ? 'open-pr' : 'push' }
  }
  switch (r.state) {
    case 'planned':
    case 'approved':
    case 'building':
    case 'reviewed':
      return { waitReason, primaryAction: agent() }
    case 'verified':
      return { waitReason, primaryAction: liveReview ? 'open-agent' : 'start-review' }
  }
  return { waitReason }
}

function summary(r: Row): TaskSummary {
  const { description: _d, externalTicket: _e, scope: _s, comments, plan: _p, planSha256: _ps, planProblems: _pp, checks: _c, proofs: _pr, diff: _df, history: _h, approvals: _a, planReadyFact, deliverGateOpen: _g, patches: _pt, ...rest } = r
  return {
    ...rest,
    planReady: r.state === 'planned' && planReadyFact,
    hasOpenQuestion: comments.some((c) => c.openQuestion),
    readOnly: !!r.branch && !r.worktree,
    sessionIds: sessions.filter((s) => s.projectId === r.projectId && s.taskId === r.id).map((s) => s.id),
    ...decide(r),
  }
}

export class FixtureApi implements Api {
  private listeners = new Set<(e: ServerEvent) => void>()

  private emit(e: ServerEvent) {
    for (const l of this.listeners) l(e)
  }

  private changed(project: string) {
    this.emit({ type: 'changed', project })
  }

  private row(pid: string, id: string): Row {
    const r = rows.find((r) => r.projectId === pid && r.id === id)
    if (!r) throw new ApiError(404, `task ${id} not found`)
    return r
  }

  private project(pid: string): Project {
    const p = projects.find((p) => p.id === pid)
    if (!p) throw new ApiError(404, `project ${pid} not found`)
    return p
  }

  private remote(pid: string) {
    return (this.project(pid).machineId ?? 'local') !== 'local'
  }

  async snapshot(): Promise<Snapshot> {
    return clone({
      clients,
      projects,
      tasks: rows.map(summary),
      sessions,
      settings,
      me: canApprove() ? { via: 'token', canApprove: true, readOnly: false } : { via: 'device', canApprove: false, deviceName: 'Galaxy S25', readOnly: false },
      queued,
      machines,
    } satisfies Snapshot)
  }

  async shellMe() {
    return null
  }

  async login() {}

  async pair(code: string, deviceName: string) {
    if (code.replace(/[-\s]/g, '').length !== 8) throw new ApiError(401, 'wrong or expired code')
    devices.push({ id: rid('d_'), name: deviceName, created: now(), lastSeen: now(), current: true })
  }

  async pairCode() {
    const abc = 'ABCDEFGHJKLMNPQRSTUVWXYZ23456789'
    const code = Array.from({ length: 8 }, () => abc[Math.floor(Math.random() * abc.length)]).join('')
    return { code, expires: new Date(Date.now() + 120_000).toISOString() }
  }

  async devices() {
    return clone(devices)
  }

  async deleteDevice(id: string) {
    const i = devices.findIndex((d) => d.id === id)
    if (i >= 0) devices.splice(i, 1)
    settings.remote.devices = settings.remote.devices.filter((d) => d.id !== id)
  }

  async rotateToken() {}

  async updateSettings(s: SettingsPatch) {
    if (s.remote && !canApprove()) throw new ApiError(403, 'this needs you at the Mac, in the Truehand app')
    if (s.language) settings.language = s.language
    if (s.defaultPermissionMode) settings.defaultPermissionMode = s.defaultPermissionMode
    if (s.remote) Object.assign(settings.remote, s.remote)
    this.emit({ type: 'waiting', count: 0 })
    return clone(settings)
  }

  async addProject(p: { path: string; clientName: string; name?: string; machineId?: string }) {
    const mid = p.machineId ?? 'local'
    const m = machines.find((m) => m.id === mid)
    if (m && m.status !== 'online') throw new ApiError(409, `${m.label} is offline`)
    const name = p.name?.trim() || p.path.replace(/\/+$/, '').split('/').pop() || p.path
    const clientId = p.clientName.trim().toLowerCase().replace(/[^a-z0-9]+/g, '-')
    if (!clients.some((c) => c.id === clientId)) clients.push({ id: clientId, name: p.clientName.trim() })
    const base = rid('p_')
    const proj: Project = { id: mid === 'local' ? base : `${mid}~${base}`, clientId, name, path: p.path, key: '', active: false, branch: 'main', machineId: mid }
    projects.push(proj)
    activations[proj.id] = { ...clone(activations.p_5e6f7a8b), gitHooks: { 'commit-msg': 'missing', 'pre-push': 'missing' } }
    this.changed(proj.id)
    return clone(proj)
  }

  async patchProject(pid: string, p: { clientName?: string; name?: string }) {
    const proj = this.project(pid)
    if (p.name) proj.name = p.name
    this.changed(pid)
    return clone(proj)
  }

  async removeProject(pid: string) {
    if (sessions.some((s) => s.projectId === pid && live(s))) throw new ApiError(409, 'the project has live sessions')
    projects.splice(projects.indexOf(this.project(pid)), 1)
    this.changed(pid)
  }

  async fsDirs(path: string) {
    const base = !path || path === '~' ? '~/Projects' : path.replace(/\/[^/]*$/, '')
    const names = ['app-cassa', 'sito-vetrina', 'truehand', 'gestionale-ordini', 'landing-2026', 'note']
    return names.map((n) => ({ name: n, path: `${base}/${n}`, isGit: n !== 'note' }))
  }

  pickFolder() {
    return null
  }

  signIntent() {
    return null
  }

  async activation(pid: string): Promise<Activation> {
    const p = this.project(pid)
    if (p.active) return { hasConfig: true, configYaml: CONFIG_YAML, stack: 'flutter', base: 'origin/main', gitHooks: { 'commit-msg': 'ours', 'pre-push': 'ours' }, configCommitted: true }
    return clone(activations[pid] ?? activations.p_5e6f7a8b)
  }

  async activate(pid: string, o: { stack?: string; base?: string; forceHooks?: boolean }) {
    const a = activations[pid] ?? activations.p_5e6f7a8b
    if (o.forceHooks && !canApprove()) throw new ApiError(403, 'replacing git hooks needs you at the Mac')
    if (Object.values(a.gitHooks).includes('foreign') && !o.forceHooks) throw new ApiError(409, 'pre-push hook is not ours')
    a.hasConfig = true
    if (o.stack) a.stack = o.stack
    if (o.base) a.base = o.base
    for (const k of Object.keys(a.gitHooks)) a.gitHooks[k] = 'ours'
    const p = this.project(pid)
    p.active = true
    p.key = 'SITO'
    this.changed(pid)
    return clone(a)
  }

  async putConfig(pid: string, yaml: string) {
    if (!canApprove()) {
      queued.push({ id: rid('q_'), kind: 'config', projectId: pid, taskId: '', device: 'Galaxy S25', time: now() })
      return { queued: true as const }
    }
    const a = activations[pid] ?? activations.p_5e6f7a8b
    a.configYaml = yaml
    a.configError = /^version:\s*0\s*$/m.test(yaml) ? undefined : 'config.yaml: version: field missing or unsupported'
    return clone(a)
  }

  async commitConfig(pid: string, configSha256: string): Promise<ActResult> {
    if (!canApprove()) {
      queued.push({ id: rid('q_'), kind: 'commit-config', projectId: pid, taskId: '', configSha256, device: 'Galaxy S25', time: now() })
      return { ok: true, queued: true }
    }
    const a = activations[pid] ?? activations.p_5e6f7a8b
    a.configCommitted = true
    a.configDiff = ''
    this.changed(pid)
    return { ok: true }
  }

  macKeyFingerprint() {
    return null
  }

  async fetchRemote(pid: string) {
    this.project(pid)
    return { ok: true, output: 'From github.com:acme/app-cassa\n * branch main -> FETCH_HEAD' }
  }

  async task(pid: string, id: string): Promise<TaskDetail> {
    const r = this.row(pid, id)
    const { planReadyFact: _f, deliverGateOpen: _g, patches: _p, ...detail } = r
    const out = clone({ ...detail, ...summary(r) })
    for (const p of out.proofs) for (const img of p.images) img.url = assets[img.url] ?? img.url
    return out
  }

  async filePatch(pid: string, id: string, path: string) {
    const r = this.row(pid, id)
    if (!(path in r.patches)) throw new ApiError(404, 'the file did not change')
    return { path, patch: r.patches[path], truncated: false }
  }

  async createTask(pid: string, n: NewTask) {
    const p = this.project(pid)
    if (!p.active) throw new ApiError(409, 'truehand is not active in this project')
    const next = Math.max(0, ...rows.filter((r) => r.projectId === pid).map((r) => Number(r.id.split('-').pop()) || 0)) + 1
    const id = n.id?.trim() || `${p.key || 'T'}-${next}`
    if (rows.some((r) => r.projectId === pid && r.id === id)) throw new ApiError(409, `task ${id} already exists`)
    const r: Row = {
      id,
      projectId: pid,
      title: n.title,
      state: 'planned',
      merged: false,
      pushed: false,
      failing: false,
      stale: false,
      updatedAt: now(),
      worktree: n.worktree !== false ? `~/.truehand/worktrees/${pid}/${id}` : undefined,
      description: n.description ?? '',
      externalTicket: n.externalTicket,
      scope: n.scope ?? [],
      comments: [],
      planProblems: [],
      checks: [],
      proofs: [],
      diff: [],
      history: [{ time: now(), type: 'created', actor: me }],
      approvals: [],
      planReadyFact: false,
      deliverGateOpen: false,
      patches: {},
    }
    rows.unshift(r)
    this.changed(pid)
    return { ...summary(r), ticketFetchError: n.externalTicket ? 'ticket not found: check LINEAR_API_KEY' : undefined }
  }

  async editTask(pid: string, id: string, p: { title?: string; description?: string; externalTicket?: string }) {
    const r = this.row(pid, id)
    if (p.title !== undefined) r.title = p.title
    if (p.description !== undefined) r.description = p.description
    if (p.externalTicket !== undefined) r.externalTicket = p.externalTicket || undefined
    r.updatedAt = now()
    this.changed(pid)
    return summary(r)
  }

  async addComment(pid: string, id: string, body: string) {
    const r = this.row(pid, id)
    const c = { id: `c${r.comments.length + 100}`, time: now(), actor: me, body }
    r.comments.push(c)
    for (const x of r.comments) x.openQuestion = false
    r.updatedAt = c.time
    this.changed(pid)
    return clone(c)
  }

  private move(r: Row, to: TaskState) {
    r.history.push({ time: now(), type: 'move', actor: me, from: r.state, to })
    r.state = to
    r.updatedAt = now()
  }

  private startSession(r: Row, role: 'dev' | 'review', mode: PermissionMode = 'default') {
    const busy = sessions.some((s) => s.projectId === r.projectId && s.taskId === r.id && s.role === role && live(s))
    if (busy) throw new ApiError(409, `a ${role} session is already running for ${r.id}`)
    const s: Session = {
      id: rid('s_'),
      projectId: r.projectId,
      taskId: r.id,
      role,
      status: 'working',
      startedAt: now(),
      resumable: false,
      permissionMode: mode,
      tail: [],
      machineId: this.project(r.projectId).machineId,
    }
    sessions.push(s)
    transcriptOf[s.id] = role === 'review' ? 'th-23-review' : 'cassa-14-dev'
    if (role === 'dev' && r.state === 'approved') this.move(r, 'building')
    this.changed(r.projectId)
    this.emit({ type: 'session', id: s.id, status: s.status })
    return s
  }

  private queue(r: Row, kind: 'approve' | 'push' | 'pr'): ActResult {
    const q = { id: rid('q_'), kind, projectId: r.projectId, taskId: r.id, planSha256: r.planSha256, device: 'Galaxy S25', time: now() }
    queued.push(q)
    this.changed(r.projectId)
    return { ok: true, queued: true, queuedId: q.id }
  }

  private run(r: Row, action: TaskAction, args: ActionArgs): ActResult {
    switch (action) {
      case 'approve-plan':
        if (args.planSha256 !== r.planSha256) throw new ApiError(409, 'the plan changed: read it again')
        r.approvals.push({ time: now(), actor: me, via: 'app+os-auth:macos', current: true })
        r.planReadyFact = false
        r.comments.forEach((c) => (c.openQuestion = false))
        this.move(r, 'approved')
        r.checks = r.checks.filter((c) => c.gate !== 'approved')
        break
      case 'push':
        r.pushed = true
        this.changed(r.projectId)
        return { ok: true, output: `To github.com:acme/app.git\n * [new branch]  th/${r.id} -> th/${r.id}\ntruehand ci verify: ok` }
      case 'open-pr':
        r.pushed = true
        r.prUrl = `https://github.com/acme/app/pull/${50 + rows.indexOf(r)}`
        this.changed(r.projectId)
        return { ok: true, url: r.prUrl }
    }
    this.changed(r.projectId)
    return { ok: true }
  }

  async act(pid: string, id: string, action: TaskAction, args: ActionArgs = {}): Promise<ActResult> {
    const r = this.row(pid, id)
    switch (action) {
      case 'start-agent':
      case 'start-review':
        return { ok: true, sessionId: this.startSession(r, action === 'start-review' ? 'review' : 'dev', args.permissionMode).id }
      case 'open-agent': {
        const s = sessions.find((s) => s.projectId === pid && s.taskId === id && live(s) && (!args.sessionId || s.id === args.sessionId))
        if (!s) throw new ApiError(404, 'no live session')
        return { ok: true, sessionId: s.id }
      }
      case 'resume-agent': {
        const s = sessions.find((s) => (args.sessionId ? s.id === args.sessionId : s.projectId === pid && s.taskId === id && s.role === 'dev' && s.resumable))
        if (!s) throw new ApiError(404, 'nothing to resume')
        await this.resumeSession(s.id)
        return { ok: true, sessionId: s.id }
      }
      case 'deliver':
        if (!r.deliverGateOpen) throw new ApiError(409, 'gate closed', undefined, { gate: r.checks })
        this.move(r, 'delivered')
        this.changed(pid)
        return { ok: true }
      case 'send-back': {
        if (!args.reason?.trim() || !args.state) throw new ApiError(400, 'state and reason are required')
        if (TASK_STATES.indexOf(args.state) >= TASK_STATES.indexOf(r.state)) throw new ApiError(400, 'can only go back')
        r.comments.push({ id: `c${r.comments.length + 100}`, time: now(), actor: me, body: args.reason })
        this.move(r, args.state)
        r.stale = false
        this.changed(pid)
        const s = sessions.find((s) => s.projectId === pid && s.taskId === id && s.role === 'dev' && live(s))
        return { ok: true, sessionId: s?.id }
      }
      case 'approve-plan':
      case 'push':
      case 'open-pr':
        if (this.remote(pid) && canApprove()) throw new ApiError(409, 'configure approvers to approve remotely', 'remote-approval-unavailable')
        if (!canApprove()) return this.queue(r, action === 'approve-plan' ? 'approve' : action === 'push' ? 'push' : 'pr')
        return this.run(r, action, action === 'approve-plan' && !args.planSha256 ? { planSha256: r.planSha256 } : args)
      case 'activate':
        throw new ApiError(400, 'activate the project with POST /api/projects/{pid}/activate')
    }
  }

  async deleteWorktree(pid: string, id: string, force?: boolean) {
    const r = this.row(pid, id)
    if (!r.merged && !force) throw new ApiError(409, 'the branch is not merged: force to delete it')
    r.worktree = undefined
    this.changed(pid)
  }

  async confirmQueued(qid: string) {
    if (!canApprove()) throw new ApiError(403, 'this needs you at the Mac, in the Truehand app')
    const i = queued.findIndex((q) => q.id === qid)
    if (i < 0) throw new ApiError(404, 'queued action not found')
    const [q] = queued.splice(i, 1)
    if (q.kind === 'config') {
      this.changed(q.projectId)
      return { ok: true }
    }
    const r = this.row(q.projectId, q.taskId)
    return this.run(r, q.kind === 'approve' ? 'approve-plan' : q.kind === 'push' ? 'push' : 'open-pr', { planSha256: q.planSha256 })
  }

  async discardQueued(qid: string) {
    const i = queued.findIndex((q) => q.id === qid)
    if (i >= 0) {
      const [q] = queued.splice(i, 1)
      this.changed(q.projectId)
    }
  }

  async startFreeSession(pid: string, o: { permissionMode?: PermissionMode } = {}) {
    const s: Session = { id: rid('s_'), projectId: pid, role: 'free', status: 'working', startedAt: now(), resumable: false, permissionMode: o.permissionMode ?? 'default', tail: [] }
    sessions.push(s)
    transcriptOf[s.id] = 'free-permission'
    this.changed(pid)
    return clone(s)
  }

  async resumeSession(sid: string) {
    const s = sessions.find((s) => s.id === sid)
    if (!s) throw new ApiError(404, 'session not found')
    if (live(s)) throw new ApiError(409, 'the session is live')
    Object.assign(s, { status: 'working', endedAt: undefined, exitCode: undefined, resumable: false })
    this.emit({ type: 'session', id: s.id, status: s.status })
    this.changed(s.projectId)
    return clone(s)
  }

  async signalSession(sid: string, sig: 'int' | 'term') {
    const s = sessions.find((s) => s.id === sid)
    if (!s) return
    if (sig === 'term') Object.assign(s, { status: 'exited', endedAt: now(), exitCode: 0, resumable: true, waitingFor: undefined })
    else Object.assign(s, { status: 'idle', waitingFor: undefined })
    this.emit({ type: 'session', id: s.id, status: s.status })
    this.changed(s.projectId)
  }

  async deleteSession(sid: string) {
    const i = sessions.findIndex((s) => s.id === sid)
    if (i >= 0) {
      const [s] = sessions.splice(i, 1)
      this.changed(s.projectId)
    }
  }

  pty(sid: string, h: PtyHandlers) {
    const s = sessions.find((s) => s.id === sid)
    const chunks = transcripts[transcriptOf[sid]] ?? []
    const running = s?.status === 'working' || s?.status === 'starting'
    const encoder = new TextEncoder()
    const timers: ReturnType<typeof setTimeout>[] = []
    let spin: ReturnType<typeof setInterval> | undefined
    let at = 0
    timers.push(setTimeout(() => h.onHello?.({ cols: 0, rows: 0, status: s?.status ?? 'exited', waitingFor: s?.waitingFor }), 0))
    for (const [delay, text] of chunks) {
      at += running ? delay : 0
      timers.push(setTimeout(() => h.onData(encoder.encode(text)), at))
    }
    if (running) {
      timers.push(
        setTimeout(() => {
          let i = 0
          const started = Date.now()
          spin = setInterval(() => {
            const secs = Math.floor((Date.now() - started) / 1000) + 12
            h.onData(`\r\x1b[2K\x1b[33m${SPINNER[i++ % SPINNER.length]}\x1b[0m Sto lavorando… \x1b[2m(${secs}s · esc per interrompere)\x1b[0m`)
          }, 120)
        }, at + 200),
      )
    } else if (s?.status === 'exited') {
      timers.push(setTimeout(() => h.onExit?.({ status: 'exited', code: s.exitCode }), at + 10))
    }
    return {
      send: (d: string) => h.onData(d === '\r' ? '\r\n' : d.replace(/\x1b\[[A-D]/, '').replace(/\x03/, '^C\r\n')),
      resize: () => {},
      close: () => {
        timers.forEach(clearTimeout)
        if (spin) clearInterval(spin)
      },
    }
  }

  async addMachine(sshTarget: string, label?: string): Promise<Machine> {
    if (!canApprove()) throw new ApiError(403, 'this needs you at the Mac, in the Truehand app')
    const m: Machine = { id: rid('m_') + 'aa', label: label?.trim() || sshTarget.split('.')[0], sshTarget, status: 'needs-update', canApprove: false }
    machines.push(m)
    this.emit({ type: 'machine', id: m.id, status: m.status })
    return clone(m)
  }

  async reconnectMachine(mid: string) {
    const m = machines.find((m) => m.id === mid)
    if (!m) throw new ApiError(404, 'machine not found')
    this.emit({ type: 'machine', id: m.id, status: m.status })
    return clone(m)
  }

  async updateMachine(mid: string) {
    const m = machines.find((m) => m.id === mid)
    if (!m) throw new ApiError(404, 'machine not found')
    m.status = 'online'
    m.version = '0.9.0'
    this.emit({ type: 'machine', id: m.id, status: m.status })
    return clone(m)
  }

  async removeMachine(mid: string) {
    const i = machines.findIndex((m) => m.id === mid)
    if (i > 0) machines.splice(i, 1)
    this.emit({ type: 'machine', id: mid, status: 'disabled' })
  }

  events(onEvent: (e: ServerEvent) => void) {
    this.listeners.add(onEvent)
    return () => void this.listeners.delete(onEvent)
  }
}
