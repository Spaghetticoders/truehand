import type {
  RestackPreview,
  OwnerAction,
  ApproverKey,
  IntentRequest,
  Passkey,
  PasskeyRegistration,
  SignedIntent,
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
import { ApiError, signAsOwner } from '../http'
import { SPINNER, transcripts } from './transcripts'
import { b64url, fromB64, serverMessage, sha256Hex, subjectOf } from '../../passkeys'
import { activations, CONFIG_YAML, devices, machines, me, passkeys, revocations, projects, queued, rows, sessions, settings, transcriptOf, clients, type Row } from './data'
import shotLogin from './shot-login.svg?url'
import shotReport from './shot-report.svg?url'
import shotToast from './shot-toast.svg?url'

const assets: Record<string, string> = { 'evidence/login.svg': shotLogin, 'evidence/report.svg': shotReport, 'evidence/toast.svg': shotToast }

const clone = <T>(v: T): T => structuredClone(v)
const now = () => new Date().toISOString()
const rid = (p: string) => `${p}${Math.random().toString(16).slice(2, 8)}`
// The fixture plays the phone at phone widths: no human factor there.
const canApprove = () => !headless() && !window.matchMedia('(max-width: 700px)').matches
// ?headless plays a Linux server without the Mac app: every human action is signed with a passkey.
const headless = () => /[?&]headless\b/.test(location.search)
const MOCK_HOST = 'truehand-box.tail1234.ts.net'
const APPROVERS: ApproverKey[] = [{ id: 'alice-s25', name: 'Alice (Galaxy S25)' }]
const hex = (n: number) => Array.from(crypto.getRandomValues(new Uint8Array(n)), (b) => b.toString(16).padStart(2, '0')).join('')
const utc = () => new Date().toISOString().replace(/\.\d+Z$/, 'Z')
const b64 = (s: string) => btoa(String.fromCharCode(...new TextEncoder().encode(s)))
const message = (header: string, fields: [string, string][]) => `${header}\n${fields.map(([k, v]) => `${k}: ${v}\n`).join('')}`
// The credential id inside authData, after "authData" in the attestation's CBOR (rpIdHash, flags, counter, aaguid, length).
// The P-256 key in the attestation's COSE key (-2: x, -3: y), as SPKI DER: what the server stores.
function spkiOf(att: Uint8Array): Uint8Array<ArrayBuffer> | null {
  for (let i = 0; i + 70 <= att.length; i++) {
    if (att[i] === 0x21 && att[i + 1] === 0x58 && att[i + 2] === 0x20 && att[i + 35] === 0x22 && att[i + 36] === 0x58 && att[i + 37] === 0x20) {
      const head = Uint8Array.from([0x30, 0x59, 0x30, 0x13, 0x06, 0x07, 0x2a, 0x86, 0x48, 0xce, 0x3d, 0x02, 0x01, 0x06, 0x08, 0x2a, 0x86, 0x48, 0xce, 0x3d, 0x03, 0x01, 0x07, 0x03, 0x42, 0x00, 0x04])
      return Uint8Array.from([...head, ...att.slice(i + 3, i + 35), ...att.slice(i + 38, i + 70)])
    }
  }
  return null
}

function credentialIdOf(att: Uint8Array): string {
  const at = new TextDecoder('latin1').decode(att).indexOf('authData') + 8
  const start = at + (att[at] === 0x59 ? 3 : 2)
  const off = start + 32 + 1 + 4 + 16
  const len = (att[off] << 8) | att[off + 1]
  return b64url(att.slice(off + 2, off + 2 + len))
}
const signatureRequired = () => new ApiError(409, 'this repo lists approvers on its base branch: confirm with a signed intent', 'signature-required', { code: 'signature-required', approvers: APPROVERS.map((a) => a.id), approverKeys: APPROVERS })
const asOwner = async (method: string, path: string, body?: unknown) => {
  if (headless()) await signAsOwner({ method, path, body: body === undefined ? '' : JSON.stringify(body) })
}
const signedOk = (si: SignedIntent | undefined, action: string) => {
  const i = si?.intent as Record<string, string> | undefined
  if (!si || si.format !== 'webauthn' || !si.sig || !si.authenticator_data || !si.client_data_json || i?.action !== action) throw new ApiError(403, `signed intent refused: not a ${action}`)
}
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

// TH-21 was built on TH-15; its chain broke (a commit no task covers), so it waits for a restack.
const stacks: Record<string, Partial<TaskSummary>> = {
  'TH-21': { stackedOn: 'TH-15', stackError: 'stack: 4f2a9c1 changes lib/board/filter.dart, which no reviewed or delivered task in the chain covers' },
  'TH-15': { tip: 'c0ffee1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7b', stackedBy: ['TH-21'] },
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
    ...(r.projectId === 'p_9c0d1e2f' ? stacks[r.id] : undefined),
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
      me: canApprove() ? { via: 'token', canApprove: true, readOnly: false } : { via: 'device', canApprove: false, deviceName: 'Galaxy S25', readOnly: false, headless: headless() },
      queued: headless() ? [] : queued,
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
    await asOwner('DELETE', `/api/devices/${id}`)
    const i = devices.findIndex((d) => d.id === id)
    if (i >= 0) devices.splice(i, 1)
    settings.remote.devices = settings.remote.devices.filter((d) => d.id !== id)
  }

  async rotateToken() {}

  async updateSettings(s: SettingsPatch) {
    if (s.remote) await asOwner('PUT', '/api/settings', s)
    if (s.remote && !canApprove() && !headless()) throw new ApiError(403, 'this needs you at the Mac, in the Truehand app')
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

  private baseHasApprovers(pid: string) {
    return /^approvers:/m.test(this.config(pid).baseYaml ?? '')
  }

  private live = new Map<string, Activation & { baseYaml: string }>()

  private config(pid: string): Activation & { baseYaml?: string } {
    if (!this.project(pid).active) return activations[pid] ?? activations.p_5e6f7a8b
    let a = this.live.get(pid)
    if (!a) {
      // The Truehand project's base already lists an approver: removing it shows what that means.
      const yaml = pid === 'p_9c0d1e2f' ? `${CONFIG_YAML}\napprovers:\n  - id: alice-mac\n    name: Alice (MacBook)\n    public_key: |\n      -----BEGIN PUBLIC KEY-----\n      MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEmac\n      -----END PUBLIC KEY-----\n` : CONFIG_YAML
      const approvers = pid === 'p_9c0d1e2f' ? [{ id: 'alice-mac', name: 'Alice (MacBook)', key: '5b1e09c4a7d2' }] : []
      a = { hasConfig: true, configYaml: yaml, baseYaml: yaml, approvers, stack: 'flutter', base: 'origin/main', gitHooks: { 'commit-msg': 'ours', 'pre-push': 'ours' }, configCommitted: true, configSha256: hex(32) }
      this.live.set(pid, a)
    }
    return a
  }


  async activation(pid: string): Promise<Activation> {
    return clone(this.config(pid))
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
    await asOwner('PUT', `/api/projects/${pid}/config`, { yaml })
    if (!canApprove() && !headless()) {
      queued.push({ id: rid('q_'), kind: 'config', projectId: pid, taskId: '', device: 'Galaxy S25', time: now() })
      return { queued: true as const }
    }
    const a = this.config(pid)
    if (a.baseYaml !== undefined && yaml !== a.configYaml) {
      const base = a.baseYaml.split('\n')
      const now = yaml.split('\n')
      const plus = now.filter((l) => !base.includes(l))
      const minus = base.filter((l) => !now.includes(l))
      a.configDiff = plus.length || minus.length ? `--- a/.truehand/config.yaml\n+++ b/.truehand/config.yaml\n@@ approvers @@\n${[...minus.map((l) => `-${l}`), ...plus.map((l) => `+${l}`)].join('\n')}\n` : ''
      a.configCommitted = !a.configDiff
      a.configSha256 = hex(32)
      const ids = (t: string) => [...t.matchAll(/- id: (\S+)(?:\n\s+name: "?([^"\n]+))?/g)].map((m) => ({ id: m[1], name: m[2] ?? m[1], key: hex(6) }))
      const baseIds = ids(a.baseYaml)
      const nowIds = ids(yaml)
      a.approversAdded = nowIds.filter((x) => !baseIds.some((y) => y.id === x.id))
      a.approversRemoved = baseIds.filter((x) => !nowIds.some((y) => y.id === x.id))
      a.approvers = nowIds
    }
    a.configYaml = yaml
    a.configError = /^version:\s*0\s*$/m.test(yaml) ? undefined : 'config.yaml: version: field missing or unsupported'
    return clone(a)
  }

  async commitConfig(pid: string, configSha256: string, signedIntent?: SignedIntent): Promise<ActResult> {
    if (headless() && !this.baseHasApprovers(pid)) await asOwner('POST', `/api/projects/${pid}/commit-config`, { configSha256 })
    else if (headless()) {
      if (!signedIntent) throw signatureRequired()
      signedOk(signedIntent, 'commit-config')
    } else if (!canApprove()) {
      queued.push({ id: rid('q_'), kind: 'commit-config', projectId: pid, taskId: '', configSha256, device: 'Galaxy S25', time: now() })
      return { ok: true, queued: true }
    }
    const a = this.config(pid)
    a.configCommitted = true
    if (!headless()) a.configDiff = ''
    this.changed(pid)
    return { ok: true }
  }

  macKeyFingerprint() {
    return null
  }

  anchorPasskey() {
    return null
  }

  revokeOwner() {
    return null
  }

  // The origin as the server names it in an intent: host/owner/repo, from the project's client and folder.
  private repoOf(pid: string) {
    const p = this.project(pid)
    return `github.com/${p.clientId}/${p.path.split('/').pop()}`
  }

  private intentFor(pid: string, action: string, task: string, sha: string, approver: string, head = ''): IntentRequest {
    const fields: [string, string][] = [
      ['action', action],
      ['repo', this.repoOf(pid)],
      ['task', task],
      ['plan_sha256', sha],
      ['head', head],
      ['nonce', hex(16)],
      ['time', utc()],
      ['approver', approver],
    ]
    return { intent: Object.fromEntries(fields), message: b64(message('truehand-intent/1', fields)) }
  }

  async configIntent(pid: string, action: 'commit-config' | 'push-base', configSha256: string, approver?: string) {
    this.project(pid)
    if (action !== 'push-base') return this.intentFor(pid, action, 'config', configSha256, approver ?? APPROVERS[0].id)
    const head = hex(20)
    const other = /[?&]otherfiles\b/.test(location.search)
    const commits = [{ sha: hex(20), subject: 'chore(truehand): add truehand config', files: ['.truehand/config.yaml'] }]
    if (other) commits.push({ sha: hex(20), subject: 'chore: update scripts', files: ['.truehand/config.yaml', 'scripts/release.sh'] })
    commits[commits.length - 1].sha = head
    const intent = this.intentFor(pid, action, 'config', configSha256, approver ?? APPROVERS[0].id, head)
    // ?nopreview plays an older server, to check the page refuses to sign blind.
    return /[?&]nopreview\b/.test(location.search) ? intent : { ...intent, push: { from: hex(20), head, commits, onlyConfig: !other } }
  }

  private restacked = new Set<string>()

  async restackPreview(pid: string, id: string): Promise<RestackPreview> {
    // TH-21 is built on TH-15, delivered but not merged yet.
    if (pid !== 'p_9c0d1e2f' || id !== 'TH-21' || this.restacked.has(id)) throw new ApiError(409, 'no commit closes a reviewed or delivered task', 'no-restack-point')
    return {
      task: id,
      from: '1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b',
      point: 'c0ffee1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7b',
      stackedOn: 'TH-15',
      predState: 'delivered',
      predTitle: this.row(pid, 'TH-15').title,
      covered: ['TH-15'],
      skipped: [
        { sha: '9d8c7b6a5f4e3d2c1b0a9f8e7d6c5b4a3f2e1d0c', subject: 'feat(board): group tasks by client' },
        { sha: '8c7b6a5f4e3d2c1b0a9f8e7d6c5b4a3f2e1d0c9b', subject: 'test(board): grouping by client' },
        { sha: 'c0ffee1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7b', subject: 'docs(board): client groups' },
      ],
      ignore: ['ios/Podfile.lock', ...Array.from({ length: 12 }, (_, i) => `tool/wakeword/data/sample-${String(i).padStart(2, '0')}.wav`), 'tool/wakeword/train.py'],
      ignoreCount: 14,
      ignoreHash: 'a3f1c9e0b7d24e6f8a1c3e5b7d9f0a2c4e6b8d0f1a3c5e7b9d1f3a5c7e9b1d3f',
    }
  }

  async restack(pid: string, id: string, p: { point: string; stackedOn: string; ignoreHash?: string }) {
    await asOwner('POST', `/api/projects/${pid}/tasks/${id}/restack`, p)
    if (!headless() && !canApprove()) {
      const q = { id: rid('q_'), kind: 'restack' as const, projectId: pid, taskId: id, device: 'Galaxy S25', time: now() }
      queued.push(q)
      this.changed(pid)
      return { ok: true, queued: true, queuedId: q.id }
    }
    this.restacked.add(id)
    stacks[id] = { stackedOn: p.stackedOn }
    this.changed(pid)
    return { ok: true, task: id, base: p.point, stackedOn: p.stackedOn }
  }

  async pushPreview(pid: string) {
    const r = await this.configIntent(pid, 'push-base', '', APPROVERS[0].id)
    return { push: r.push, approvers: this.baseHasApprovers(pid) ? APPROVERS.map((a) => a.id) : [], approverKeys: this.baseHasApprovers(pid) ? APPROVERS : [] }
  }

  async pushBase(pid: string, signedIntent?: SignedIntent, shown?: { from: string; head: string }): Promise<ActResult> {
    if (!signedIntent && headless() && !(shown?.from && shown.head)) throw new ApiError(409, 'push-base needs the from and head that were shown', 'base-moved')
    const head = shown?.head
    if (headless() && !this.baseHasApprovers(pid)) {
      await asOwner('POST', `/api/projects/${pid}/push-base`, { from: shown?.from, head })
      if (/[?&]basemoved\b/.test(location.search)) throw new ApiError(409, `base moved: origin/main is now ${hex(20)}, not the commit you confirmed`, 'base-moved')
    }
    else if (headless()) {
      if (!signedIntent) throw signatureRequired()
      signedOk(signedIntent, 'push-base')
    } else if (!canApprove()) throw new ApiError(403, 'this needs you at the Mac, in the Truehand app')
    const a = this.config(pid)
    a.configDiff = ''
    a.approversAdded = []
    a.approversRemoved = []
    if (a.baseYaml !== undefined) a.baseYaml = a.configYaml
    this.changed(pid)
    return { ok: true, output: 'To github.com:acme/app.git\n   1a2b3c4..5d6e7f8  main -> main' }
  }

  async taskIntent(pid: string, id: string, action: TaskAction, approver: string) {
    const r = this.row(pid, id)
    const head = action === 'approve-plan' ? '' : hex(20)
    return { ...this.intentFor(r.projectId, action, r.id, r.planSha256 ?? '', approver, head), title: r.title, branch: action === 'approve-plan' ? '' : `th/${r.id}` }
  }

  private host(p: Passkey): Passkey {
    return { ...p, rpId: p.rpId === MOCK_HOST ? location.hostname : p.rpId }
  }

  async passkeys() {
    const list = passkeys.map((p) => this.host(p))
    // A broken revocation list suspends every owner, as on the server.
    return { rpId: location.hostname, headless: headless(), hasOwner: !/[?&]revocerr\b/.test(location.search) && list.some((p) => p.ownerValid), passkeys: list, revocations: clone(revocations), revocationsError: /[?&]revocerr\b/.test(location.search) ? '/etc/truehand/revoked.json: unexpected end of JSON input' : undefined, revocationsCode: /[?&]revocerr\b/.test(location.search) ? ('revoked-list-invalid' as const) : undefined }
  }

  async passkeyCode(owner: boolean) {
    await asOwner('POST', '/api/passkeys/enroll-code', { owner })
    return { ...(await this.pairCode()), owner }
  }

  private ceremonies = new Map<string, { label: string; owner: boolean }>()

  async passkeyOptions(o: { code: string; label: string }) {
    if (o.code.replace(/[-\s]/g, '').length !== 8) throw new ApiError(401, 'wrong or expired code')
    const ceremony = rid('c_')
    const owner = !passkeys.some((p) => p.ownerValid) || /^[A-H]/.test(o.code)
    this.ceremonies.set(ceremony, { label: o.label, owner })
    const u64 = (n: number) => btoa(hex(n)).replace(/=+$/, '').replace(/\+/g, '-').replace(/\//g, '_')
    return {
      ceremony,
      owner,
      publicKey: {
        challenge: u64(32),
        rp: { id: location.hostname, name: 'Truehand' },
        user: { id: u64(16), name: 'truehand@box', displayName: 'Truehand box' },
        pubKeyCredParams: [
          { type: 'public-key' as const, alg: -7 },
          { type: 'public-key' as const, alg: -257 },
        ],
        timeout: 300_000,
        excludeCredentials: passkeys.filter((p) => this.host(p).rpId === location.hostname).map((p) => ({ type: 'public-key' as const, id: p.id })),
        authenticatorSelection: { userVerification: 'required' as const, residentKey: 'preferred' as const },
        attestation: 'none' as const,
      },
    }
  }

  async registerPasskey(r: PasskeyRegistration) {
    const c = this.ceremonies.get(r.ceremony)
    this.ceremonies.delete(r.ceremony)
    if (!c) throw new ApiError(401, 'registration expired: start again')
    const cdj = JSON.parse(new TextDecoder().decode(fromB64(r.clientDataJSON)))
    if (cdj.type !== 'webauthn.create') throw new ApiError(400, 'passkey refused: not a registration')
    const att = fromB64(r.attestationObject)
    const id = credentialIdOf(att)
    const der = spkiOf(att)
    const publicKey = der ? `-----BEGIN PUBLIC KEY-----\n${btoa(String.fromCharCode(...der))}\n-----END PUBLIC KEY-----` : `-----BEGIN PUBLIC KEY-----\nMFkw${hex(30)}\n-----END PUBLIC KEY-----`
    const key = der ? (await sha256Hex(der)).slice(0, 12) : hex(6)
    const p: Passkey = {
      id,
      label: c.label,
      rpId: location.hostname,
      alg: 'ES256',
      owner: c.owner,
      ownerValid: c.owner,
      synced: false,
      syncable: true,
      signCount: 0,
      device: 'd_here',
      thisDevice: true,
      key,
      publicKey,
      created: now(),
    }
    passkeys.push(p)
    if (!devices.some((d) => d.id === 'd_here')) devices.push({ id: 'd_here', name: c.label, created: now(), lastSeen: now(), current: true })
    return clone(p)
  }

  async deletePasskey(id: string, owner?: string) {
    if (!owner) await asOwner('DELETE', `/api/passkeys/${id}`)
    const i = passkeys.findIndex((p) => p.id === id)
    if (i >= 0) {
      const [p] = passkeys.splice(i, 1)
      if (p.owner && p.ownerValid) revocations.unshift({ id: p.id, key: p.key, label: p.label, revoked: 'soft', time: now() })
    }
    return { orphaned: [] }
  }

  async grantPasskey(id: string, owner: string) {
    if (!owner) throw new ApiError(403, 'confirm with an owner\'s passkey')
    const p = passkeys.find((p) => p.id === id)
    if (!p || !p.owner || p.ownerValid) throw new ApiError(409, 'that passkey is not waiting to become an owner')
    p.ownerValid = true
    p.pending = undefined
  }

  async ownerChallenge(r: { credential: string; action: OwnerAction; method?: string; path?: string; bodySha256?: string; passkey?: string }) {
    const target = passkeys.find((p) => p.id === r.passkey)
    const subject =
      r.action === 'owner'
        ? await subjectOf({ body_sha256: r.bodySha256 ?? '', method: r.method ?? '', path: r.path ?? '' })
        : r.action === 'add-owner' && target
          ? await subjectOf({ credential: target.id, public_key_sha256: await sha256Hex(target.publicKey.trim()), rp_id: this.host(target).rpId })
          : hex(32)
    const intent = { action: r.action, server: location.hostname, subject, nonce: hex(16), time: utc(), credential: r.credential }
    const bytes = serverMessage(intent)
    const challenge = new Uint8Array(await crypto.subtle.digest('SHA-256', bytes))
    const u = (b: Uint8Array) => btoa(String.fromCharCode(...b)).replace(/=+$/, '').replace(/\+/g, '-').replace(/\//g, '_')
    return { intent, key: target?.key, publicKey: { challenge: u(challenge), rpId: location.hostname, allowCredentials: [{ type: 'public-key' as const, id: r.credential }], userVerification: 'required', timeout: 120_000 } }
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
        if (headless()) {
          if (!args.signedIntent) throw signatureRequired()
          signedOk(args.signedIntent, action)
          return this.run(r, action, args)
        }
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
