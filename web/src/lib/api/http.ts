import type { ActResult, Api, ActionArgs, IntentRequest, Me, NewTask, PtyHandlers, ServerEvent, SettingsPatch, RestackPreview, ScopePreview, SignedIntent, TaskAction } from './types'

export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
    readonly code?: string,
    readonly body?: unknown,
  ) {
    super(message)
  }
}

interface ShellCore {
  invoke<T>(cmd: string, args?: Record<string, unknown>): Promise<T>
}

/** The Mac app's bridge: requests that need the human factor go through it, so its secret never reaches JS. */
export const shell = (): ShellCore | undefined => (window as unknown as { __TAURI__?: { core?: ShellCore } }).__TAURI__?.core

const SEG = '[A-Za-z0-9_.~-]+'
const VIA_SHELL = [
  `GET /api/me`,
  `POST /api/projects/${SEG}/tasks/${SEG}/actions/(approve-plan|push|open-pr)`,
  `POST /api/queued/${SEG}/confirm`,
  `POST /api/pair/code`,
  `PUT /api/settings`,
  `PUT /api/projects/${SEG}/config`,
  `POST /api/projects/${SEG}/commit-config`,
  `POST /api/projects/${SEG}/push-base`,
  `POST /api/projects/${SEG}/tasks/${SEG}/restack`,
  `POST /api/projects/${SEG}/tasks/${SEG}/scope`,
  `POST /api/passkeys/enroll-code`,
  `DELETE /api/passkeys/${SEG}`,
  `POST /api/token/rotate`,
  `DELETE /api/devices/${SEG}`,
  `POST /api/projects/${SEG}/activate`,
  `POST /api/machines`,
  `POST /api/machines/${SEG}/update`,
].map((p) => new RegExp(`^${p}$`))

const enc = encodeURIComponent

/** Signs one request with an owner passkey and returns the X-Truehand-Owner header, where the server has no Mac app. */
export type OwnerSigner = (r: { method: string; path: string; body: string }) => Promise<string>

let ownerSigner: OwnerSigner | null = null

export function setOwnerSigner(f: OwnerSigner | null) {
  ownerSigner = f
}

export const OWNER_HEADER = 'X-Truehand-Owner'

/** The mock plays a headless server with this: it asks for the owner's signature the way a 409 owner-required does. */
export function signAsOwner(r: { method: string; path: string; body: string }) {
  if (!ownerSigner) throw new ApiError(409, 'owner passkey required', 'owner-required')
  return ownerSigner(r)
}

export class HttpApi implements Api {
  private listeners = new Set<(e: ServerEvent) => void>()

  private async req<T>(method: string, path: string, body?: unknown, owner?: string): Promise<T> {
    const core = shell()
    const route = `${method} ${path.split('?')[0]}`
    let status: number
    let data: { error?: string; code?: string } | undefined
    if (core && VIA_SHELL.some((re) => re.test(route))) {
      const r = await core.invoke<{ status: number; body: unknown }>('shell_request', { method, path, body: body ?? null })
      status = r.status
      data = (r.body ?? undefined) as typeof data
    } else {
      const text = body === undefined ? undefined : JSON.stringify(body)
      const headers: Record<string, string> = {}
      if (text !== undefined) headers['Content-Type'] = 'application/json'
      if (owner) headers[OWNER_HEADER] = owner
      const res = await fetch(path, { method, credentials: 'same-origin', headers, body: text })
      status = res.status
      const reply = await res.text()
      data = reply ? JSON.parse(reply) : undefined
      // No Mac app on this server: the person signs this very request with an owner passkey, then it goes again.
      if (status === 409 && data?.code === 'owner-required' && !owner && ownerSigner) {
        return this.req<T>(method, path, body, await ownerSigner({ method, path, body: text ?? '' }))
      }
    }
    if (status >= 400) throw new ApiError(status, data?.error ?? `HTTP ${status}`, data?.code, data)
    // Our own changes show at once, without waiting for the server's event.
    if (method !== 'GET') for (const l of this.listeners) l({ type: 'changed', project: '' })
    if (status === 202) return { queued: true, ...(data as object) } as T
    return data as T
  }

  private t(pid: string, id: string) {
    return `/api/projects/${enc(pid)}/tasks/${enc(id)}`
  }

  snapshot = () => this.req<Awaited<ReturnType<Api['snapshot']>>>('GET', '/api/snapshot')
  shellMe = async () => (shell() ? this.req<Me>('GET', '/api/me') : null)
  login = (token: string) =>
    fetch('/api/login', { method: 'POST', credentials: 'same-origin', headers: { Authorization: `Bearer ${token}` } }).then((r) => {
      if (!r.ok) throw new ApiError(r.status, `HTTP ${r.status}`)
    })
  pair = (code: string, deviceName: string) => this.req<void>('POST', '/api/pair', { code, deviceName })
  pairCode = () => this.req<{ code: string; expires: string }>('POST', '/api/pair/code')
  devices = () => this.req<Awaited<ReturnType<Api['devices']>>>('GET', '/api/devices')
  deleteDevice = (id: string) => this.req<void>('DELETE', `/api/devices/${enc(id)}`)
  rotateToken = () => this.req<void>('POST', '/api/token/rotate')
  updateSettings = (s: SettingsPatch) => this.req<Awaited<ReturnType<Api['updateSettings']>>>('PUT', '/api/settings', s)

  addProject = (p: { path: string; clientName: string; name?: string; machineId?: string }) => this.req<Awaited<ReturnType<Api['addProject']>>>('POST', '/api/projects', p)
  patchProject = (pid: string, p: { clientName?: string; name?: string }) => this.req<Awaited<ReturnType<Api['patchProject']>>>('PATCH', `/api/projects/${enc(pid)}`, p)
  removeProject = (pid: string) => this.req<void>('DELETE', `/api/projects/${enc(pid)}`)
  fsDirs = (path: string, machineId?: string) =>
    this.req<Awaited<ReturnType<Api['fsDirs']>>>('GET', `${machineId && machineId !== 'local' ? `/api/machines/${enc(machineId)}` : '/api'}/fs/dirs?path=${enc(path)}`)
  pickFolder = () => {
    const core = shell()
    return core ? core.invoke<string | null>('pick_folder') : null
  }
  signIntent = (projectId: string, taskId: string, action: TaskAction, approver?: string) => {
    const core = shell()
    return core ? core.invoke<SignedIntent>('sign_intent', { projectId, taskId, action, approver }) : null
  }
  activation = (pid: string) => this.req<Awaited<ReturnType<Api['activation']>>>('GET', `/api/projects/${enc(pid)}/activation`)
  activate = (pid: string, o: { stack?: string; base?: string; forceHooks?: boolean }) => this.req<Awaited<ReturnType<Api['activate']>>>('POST', `/api/projects/${enc(pid)}/activate`, o)
  putConfig = (pid: string, yaml: string) => this.req<Awaited<ReturnType<Api['putConfig']>>>('PUT', `/api/projects/${enc(pid)}/config`, { yaml })
  commitConfig = (pid: string, configSha256: string, signedIntent?: SignedIntent) =>
    this.req<ActResult>('POST', `/api/projects/${enc(pid)}/commit-config`, signedIntent ? { configSha256, signedIntent } : { configSha256 })
  configIntent = (pid: string, action: 'commit-config' | 'push-base', configSha256: string, approver?: string) =>
    this.req<IntentRequest>('POST', `/api/projects/${enc(pid)}/config-intent`, { action, configSha256, approver })
  pushBase = (pid: string, signedIntent?: SignedIntent, shown?: { from: string; head: string }) =>
    this.req<ActResult>('POST', `/api/projects/${enc(pid)}/push-base`, signedIntent ? { signedIntent } : { from: shown?.from, head: shown?.head })
  scopePreview = (pid: string, id: string) => this.req<ScopePreview>('GET', `${this.t(pid, id)}/scope`)
  widenScope = (pid: string, id: string, p: { add: string[] }) => this.req<Awaited<ReturnType<Api['widenScope']>>>('POST', `${this.t(pid, id)}/scope`, p)
  restackPreview = (pid: string, id: string) => this.req<RestackPreview>('GET', `${this.t(pid, id)}/restack`)
  restack = (pid: string, id: string, p: { point: string; stackedOn: string; ignoreHash?: string }) => this.req<Awaited<ReturnType<Api['restack']>>>('POST', `${this.t(pid, id)}/restack`, p)
  pushPreview = (pid: string) => this.req<Awaited<ReturnType<Api['pushPreview']>>>('GET', `/api/projects/${enc(pid)}/push-base`)
  taskIntent = (pid: string, id: string, action: TaskAction, approver: string) => this.req<IntentRequest>('POST', `${this.t(pid, id)}/intent`, { action, approver })

  passkeys = () => this.req<Awaited<ReturnType<Api['passkeys']>>>('GET', '/api/passkeys')
  passkeyCode = (owner: boolean) => this.req<Awaited<ReturnType<Api['passkeyCode']>>>('POST', '/api/passkeys/enroll-code', { owner })
  passkeyOptions = (o: { code: string; label: string }) => this.req<Awaited<ReturnType<Api['passkeyOptions']>>>('POST', '/api/passkeys/register/options', o)
  registerPasskey = (r: Parameters<Api['registerPasskey']>[0]) => this.req<Awaited<ReturnType<Api['registerPasskey']>>>('POST', '/api/passkeys/register', r)
  deletePasskey = (id: string, owner?: string) => this.req<{ orphaned: string[] }>('DELETE', `/api/passkeys/${enc(id)}`, undefined, owner)
  grantPasskey = (id: string, owner: string) => this.req<void>('POST', `/api/passkeys/${enc(id)}/grant`, undefined, owner)
  ownerChallenge = (r: Parameters<Api['ownerChallenge']>[0]) => this.req<Awaited<ReturnType<Api['ownerChallenge']>>>('POST', '/api/owner/challenge', r)
  macKeyFingerprint = () => {
    const core = shell()
    return core ? core.invoke<string | null>('approver_key_fingerprint') : null
  }
  anchorPasskey = (id: string) => {
    const core = shell()
    return core ? core.invoke<void>('anchor_passkey', { id }) : null
  }
  revokeOwner = (passkeyId: string) => {
    const core = shell()
    return core ? core.invoke<void>('revoke_owner', { passkeyId }) : null
  }
  fetchRemote = (pid: string) => this.req<{ ok: boolean; output: string }>('POST', `/api/projects/${enc(pid)}/fetch`)

  task = (pid: string, id: string, branch?: string) => this.req<Awaited<ReturnType<Api['task']>>>('GET', this.t(pid, id) + (branch ? `?branch=${enc(branch)}` : ''))
  filePatch = (pid: string, id: string, path: string) => this.req<Awaited<ReturnType<Api['filePatch']>>>('GET', `${this.t(pid, id)}/diff?file=${enc(path)}`)
  createTask = (pid: string, t: NewTask) => this.req<Awaited<ReturnType<Api['createTask']>>>('POST', `/api/projects/${enc(pid)}/tasks`, t)
  editTask = (pid: string, id: string, p: { title?: string; description?: string; externalTicket?: string }) => this.req<Awaited<ReturnType<Api['editTask']>>>('PATCH', this.t(pid, id), p)
  addComment = (pid: string, id: string, body: string, sendToSession?: string) =>
    this.req<Awaited<ReturnType<Api['addComment']>>>('POST', `${this.t(pid, id)}/comments`, sendToSession ? { body, sendToSession } : { body })
  act = (pid: string, id: string, action: TaskAction, args?: ActionArgs) => this.req<ActResult>('POST', `${this.t(pid, id)}/actions/${action}`, args ?? {})
  deleteWorktree = (pid: string, id: string, force?: boolean) => this.req<void>('DELETE', `${this.t(pid, id)}/worktree${force ? '?force=1' : ''}`)
  confirmQueued = (qid: string, signedIntent?: SignedIntent) =>
    this.req<ActResult>('POST', `/api/queued/${enc(qid)}/confirm`, signedIntent ? { signedIntent } : undefined)
  discardQueued = (qid: string) => this.req<void>('DELETE', `/api/queued/${enc(qid)}`)

  startFreeSession = (pid: string, o?: object) => this.req<Awaited<ReturnType<Api['startFreeSession']>>>('POST', `/api/projects/${enc(pid)}/sessions`, o ?? {})
  resumeSession = (sid: string, o?: object) => this.req<Awaited<ReturnType<Api['resumeSession']>>>('POST', `/api/sessions/${enc(sid)}/resume`, o ?? {})
  signalSession = (sid: string, sig: 'int' | 'term') => this.req<void>('POST', `/api/sessions/${enc(sid)}/signal`, { sig })
  deleteSession = (sid: string) => this.req<void>('DELETE', `/api/sessions/${enc(sid)}`)

  pty(sid: string, h: PtyHandlers) {
    let ws: WebSocket
    let closed = false
    let size: [number, number] | null = null
    const queue: string[] = []
    const send = (m: object) => {
      const s = JSON.stringify(m)
      if (ws.readyState === WebSocket.OPEN) ws.send(s)
      else queue.push(s)
    }
    const connect = () => {
      ws = new WebSocket(`${location.protocol === 'https:' ? 'wss:' : 'ws:'}//${location.host}/api/sessions/${enc(sid)}/pty`)
      ws.binaryType = 'arraybuffer'
      ws.onopen = () => {
        for (const m of queue.splice(0)) ws.send(m)
      }
      ws.onmessage = (e) => {
        if (typeof e.data !== 'string') return h.onData(new Uint8Array(e.data as ArrayBuffer))
        const m = JSON.parse(e.data)
        if (m.t === 'hello') h.onHello?.(m)
        else if (m.t === 'exit') h.onExit?.({ status: m.status, code: m.code })
      }
      ws.onclose = (e) => {
        // 1013: this viewer fell behind; the server wants a fresh connection (with scrollback).
        if (!closed && e.code === 1013) {
          h.onData('\x1bc')
          connect()
          if (size) send({ t: 'resize', c: size[0], r: size[1] })
        }
      }
    }
    connect()
    return {
      send: (d: string) => send({ t: 'in', d }),
      resize: (c: number, r: number) => {
        size = [c, r]
        send({ t: 'resize', c, r })
      },
      close: () => {
        closed = true
        ws.close()
      },
    }
  }

  addMachine = (sshTarget: string, label?: string) => this.req<Awaited<ReturnType<Api['addMachine']>>>('POST', '/api/machines', { sshTarget, label })
  reconnectMachine = (mid: string) => this.req<Awaited<ReturnType<Api['reconnectMachine']>>>('POST', `/api/machines/${enc(mid)}/reconnect`)
  updateMachine = (mid: string) => this.req<Awaited<ReturnType<Api['updateMachine']>>>('POST', `/api/machines/${enc(mid)}/update`)
  removeMachine = (mid: string) => this.req<void>('DELETE', `/api/machines/${enc(mid)}`)

  events(onEvent: (e: ServerEvent) => void) {
    let es: EventSource
    let stopped = false
    let retry: ReturnType<typeof setTimeout> | undefined
    const data = (e: Event) => (e as MessageEvent).data as string
    const connect = () => {
      es = new EventSource('/api/events', { withCredentials: true })
      es.addEventListener('changed', (e) => onEvent({ type: 'changed', project: data(e) }))
      es.addEventListener('session', (e) => onEvent({ type: 'session', ...JSON.parse(data(e)) }))
      es.addEventListener('waiting', (e) => onEvent({ type: 'waiting', count: JSON.parse(data(e)).count }))
      es.addEventListener('machine', (e) => onEvent({ type: 'machine', ...JSON.parse(data(e)) }))
      es.addEventListener('passkeys', () => onEvent({ type: 'passkeys' }))
      // The browser gives up after a 401 (not signed in yet): try again until it works.
      es.onopen = () => onEvent({ type: 'connection', up: true })
      es.onerror = () => {
        onEvent({ type: 'connection', up: false })
        if (es.readyState === EventSource.CLOSED && !stopped) retry = setTimeout(connect, 3000)
      }
    }
    connect()
    this.listeners.add(onEvent)
    return () => {
      stopped = true
      clearTimeout(retry)
      es.close()
      this.listeners.delete(onEvent)
    }
  }
}
