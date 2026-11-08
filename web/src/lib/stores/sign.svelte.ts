import { api, type ApproverKey, type IntentRequest, type OwnerAction, type Passkey, type SignedIntent, type TaskAction } from '../api'
import { setOwnerSigner, shell } from '../api/http'
import { t, type Key } from '../i18n/index.svelte'
import {
  b64url,
  expect,
  fromB64,
  intentOf,
  known,
  parseMessage,
  pemFingerprint,
  PasskeyError,
  serverMessage,
  sha256Hex,
  sign,
  stdB64,
  subjectOf,
  toHex,
  toStdB64,
  type Assertion,
  type Message,
  type SignRequest,
} from '../passkeys'

export type SignKind = 'task' | 'config' | 'owner'

export interface SignContext {
  title?: string
  branch?: string
  /** The subject was recomputed here from what the person sees. */
  verified?: boolean
  /** Fingerprint of the passkey the signature is about, to compare with its phone. */
  key?: string
  /** What the change does, as the page shows it. */
  diff?: string
  push?: import('../api').PushPreview
  restack?: import('../api').RestackPreview
  /** The owner passkey picked is not known on this device: a failure most likely means it is elsewhere. */
  elsewhere?: string
}

const isMine = (p: Passkey) => p.thisDevice || known().includes(p.id)

export interface Choice {
  id: string
  name: string
}

export type Pending =
  | { mode: 'choose'; kind: 'approver' | 'owner'; options: Choice[]; resolve: (id: string) => void; reject: (e: unknown) => void }
  | {
      mode: 'sign'
      kind: SignKind
      message: Message
      raw: string
      req: SignRequest
      context: SignContext
      busy: boolean
      error: unknown
      resolve: (a: Assertion) => void
      reject: (e: unknown) => void
    }

export const signing = $state<{ pending: Pending | null }>({ pending: null })

/** True where human actions are signed with a passkey in this browser rather than by the Mac app. */
export const passkeyHere = () => !shell()

function choose(kind: 'approver' | 'owner', options: Choice[]): Promise<string> {
  return new Promise((resolve, reject) => {
    signing.pending = { mode: 'choose', kind, options, resolve, reject }
  })
}

async function chooseApprover(keys: ApproverKey[]): Promise<ApproverKey> {
  if (!keys.length) throw new PasskeyError('failed', 'no approvers')
  if (keys.length === 1) return keys[0]
  const mine = known()
  const own = keys.filter((k) => k.credentialId && mine.includes(k.credentialId))
  if (own.length === 1) return own[0]
  const id = await choose(
    'approver',
    (own.length ? own : keys).map((k) => ({ id: k.id, name: k.name || k.id })),
  )
  return keys.find((k) => k.id === id)!
}

/** The owner passkey to sign with: one this browser registered when it can tell, else the person picks. */
async function chooseOwner(): Promise<Passkey> {
  const list = await api.passkeys()
  const owners = list.passkeys.filter((p) => p.owner && p.ownerValid && p.rpId === list.rpId)
  if (!owners.length) throw new PasskeyError('no-owner')
  const mine = known()
  const own = owners.filter((p) => p.thisDevice || mine.includes(p.id))
  if (own.length === 1) return own[0]
  // A passkey bound to another device cannot answer here; a synced one might.
  const reachable = own.length ? own : owners.filter((p) => p.synced)
  if (!reachable.length) throw new PasskeyError('elsewhere', owners.map((p) => p.label).join(', '))
  if (reachable.length === 1) return reachable[0]
  const id = await choose(
    'owner',
    reachable.map((p) => ({ id: p.id, name: p.label })),
  )
  return owners.find((p) => p.id === id)!
}

/** Shows what the bytes say and waits for the person to sign them (a tap is needed for the browser to ask). */
function confirm(kind: SignKind, req: SignRequest, context: SignContext, check: (m: Message) => void): Promise<{ message: Message; assertion: Assertion }> {
  const bytes = fromB64(req.message)
  const message = parseMessage(bytes)
  check(message)
  const raw = new TextDecoder().decode(bytes)
  return new Promise((resolve, reject) => {
    signing.pending = { mode: 'sign', kind, message, raw, req, context, busy: false, error: null, resolve: (assertion) => resolve({ message, assertion }), reject }
  })
}

export async function signPending() {
  const p = signing.pending
  if (p?.mode !== 'sign' || p.busy) return
  p.busy = true
  p.error = null
  try {
    const a = await sign(p.req)
    signing.pending = null
    p.resolve(a)
  } catch (e) {
    p.busy = false
    p.error = p.context.elsewhere && e instanceof PasskeyError && (e.code === 'cancelled' || e.code === 'failed') ? new PasskeyError('elsewhere', p.context.elsewhere) : e
  }
}

export function cancelPending() {
  const p = signing.pending
  if (!p) return
  signing.pending = null
  p.reject(new PasskeyError('cancelled'))
}

const signedOf = (message: Message, a: Assertion) => ({
  intent: intentOf(message),
  format: 'webauthn' as const,
  sig: toStdB64(a.signature),
  authenticator_data: a.authenticatorData,
  client_data_json: a.clientDataJSON,
})

function requestOf(r: IntentRequest, key: ApproverKey): SignRequest {
  const ids = r.allowCredentials?.map((c) => c.id) ?? []
  if (!ids.length && key.credentialId) ids.push(key.credentialId)
  return { message: r.message, rpId: r.rpId || key.rpId || undefined, allowCredentials: ids.length ? ids : undefined }
}

/** approvers: the 409 signature-required body, either ids or keys with their credential. */
export const approverKeys = (body: unknown): ApproverKey[] => {
  const b = body as { approverKeys?: ApproverKey[]; approvers?: string[] } | undefined
  return b?.approverKeys?.length ? b.approverKeys : (b?.approvers ?? []).map((id) => ({ id }))
}

export async function signTask(pid: string, id: string, action: TaskAction, keys: ApproverKey[], planSha256?: string): Promise<SignedIntent> {
  const key = await chooseApprover(keys)
  const r = await api.taskIntent(pid, id, action, key.id)
  const { message, assertion } = await confirm('task', requestOf(r, key), { title: r.title, branch: r.branch }, (m) =>
    expect(m, { action, task: id, approver: key.id, plan_sha256: planSha256 }),
  )
  return signedOf(message, assertion)
}

export async function signConfig(pid: string, action: 'commit-config' | 'push-base', configSha256: string, keys: ApproverKey[], title?: string, diff?: string): Promise<SignedIntent> {
  const key = await chooseApprover(keys)
  const r = await api.configIntent(pid, action, configSha256, key.id)
  // Without the preview the person cannot see what leaves: never sign blind.
  if (action === 'push-base' && !r.push?.head) throw new PasskeyError('no-preview')
  // push-base names the config committed on the base, which the server reads; commit-config names the one on screen.
  const { message, assertion } = await confirm('config', requestOf(r, key), { title, diff, push: r.push }, (m) =>
    expect(m, { action, task: 'config', approver: key.id, plan_sha256: action === 'commit-config' ? configSha256 : undefined, head: r.push?.head }),
  )
  return signedOf(message, assertion)
}

let ownerExtra: Pick<SignContext, 'diff' | 'push' | 'restack'> = {}

/** Runs fn with what the page shows about the change, so an owner signature of it shows it too. */
export async function withOwnerContext<T>(extra: Pick<SignContext, 'diff' | 'push' | 'restack'>, fn: () => Promise<T>): Promise<T> {
  ownerExtra = extra
  try {
    return await fn()
  } finally {
    ownerExtra = {}
  }
}

/** Signs a server intent with an owner passkey and returns the X-Truehand-Owner header. */
async function ownerSign(
  action: OwnerAction,
  params: { method?: string; path?: string; bodySha256?: string; passkey?: string },
  title: string,
  subject?: Record<string, string>,
  key?: string,
) {
  const by = await chooseOwner()
  const c = await api.ownerChallenge({ credential: by.id, action, ...params })
  if (key && c.key && c.key !== key) throw new PasskeyError('mismatch', `key ${c.key} ≠ ${key}`)
  const bytes = c.message ? fromB64(c.message) : serverMessage(c.intent)
  // What the authenticator signs is the challenge the server chose: it must be the hash of these bytes.
  if ((await sha256Hex(bytes)) !== toHex(fromB64(c.publicKey.challenge))) throw new PasskeyError('mismatch', 'challenge')
  const want = subject ? await subjectOf(subject) : undefined
  const { message, assertion } = await confirm(
    'owner',
    { message: stdB64(bytes), rpId: c.publicKey.rpId, allowCredentials: c.publicKey.allowCredentials.map((x) => x.id) },
    { title, verified: !!want, key, elsewhere: isMine(by) ? undefined : by.label, ...(action === 'owner' ? ownerExtra : {}) },
    (m) => {
      expect(m, { action, credential: by.id, server: location.hostname, subject: want })
      // The bytes and the intent the header will carry must say the same thing.
      expect(m, c.intent)
      if (m.fields.length !== Object.keys(c.intent).length) throw new PasskeyError('mismatch', 'intent')
    },
  )
  return b64url(new TextEncoder().encode(JSON.stringify(signedOf(message, assertion))))
}

export async function ownerHeader(action: 'add-owner' | 'revoke-passkey', p: Passkey) {
  const key = await pemFingerprint(p.publicKey)
  // add-owner binds the passkey (appstate Passkey.Binding): recomputed here from the key shown.
  const subject = action === 'add-owner' ? { credential: p.id, public_key_sha256: await sha256Hex(p.publicKey.trim()), rp_id: p.rpId } : undefined
  return ownerSign(action, { passkey: p.id }, t(`passkey.ownerTitle.${action}`, { label: p.label }), subject, key)
}

const REQUESTS: [RegExp, Key][] = [
  [/^PUT \/api\/settings$/, 'passkey.request.settings'],
  [/^POST \/api\/pair\/code$/, 'passkey.request.pairCode'],
  [/^DELETE \/api\/devices\//, 'passkey.request.revokeDevice'],
  [/^POST \/api\/token\/rotate$/, 'passkey.request.rotate'],
  [/^POST \/api\/passkeys\/enroll-code$/, 'passkey.request.enrollCode'],
  [/^DELETE \/api\/passkeys\//, 'passkey.request.revokePasskey'],
  [/^PUT \/api\/projects\/[^/]+\/config$/, 'passkey.request.putConfig'],
  [/^POST \/api\/projects\/[^/]+\/commit-config$/, 'passkey.request.commitConfig'],
  [/^POST \/api\/projects\/[^/]+\/push-base$/, 'passkey.request.pushBase'],
  [/^POST \/api\/projects\/[^/]+\/activate$/, 'passkey.request.activate'],
  [/^POST \/api\/machines/, 'passkey.request.machines'],
  [/^POST \/api\/projects\/[^/]+\/tasks\/[^/]+\/actions\//, 'passkey.request.taskAction'],
  [/^POST \/api\/projects\/[^/]+\/tasks\/[^/]+\/restack$/, 'passkey.request.restack'],
]

const TASK_ACTION = /^POST \/api\/projects\/[^/]+\/tasks\/([^/]+)\/actions\/(approve-plan|push|open-pr)$/

export const requestTitle = (method: string, path: string) => {
  const line = `${method} ${path.split('?')[0]}`
  const m = TASK_ACTION.exec(line)
  if (m) return t('passkey.request.taskActionOf', { action: t(`passkey.actionName.${m[2] as 'approve-plan' | 'push' | 'open-pr'}`), task: decodeURIComponent(m[1]) })
  const k = REQUESTS.find(([re]) => re.test(line))?.[1]
  return k ? t(k) : line
}

if (passkeyHere()) {
  setOwnerSigner(async ({ method, path, body }) => {
    const bodySha256 = await sha256Hex(body)
    return ownerSign('owner', { method, path, bodySha256 }, requestTitle(method, path), { body_sha256: bodySha256, method, path })
  })
}
