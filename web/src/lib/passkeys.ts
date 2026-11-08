// WebAuthn in the browser: registering a passkey and signing the exact bytes the server will verify.

export type PasskeyErrorCode = 'cancelled' | 'timeout' | 'unsupported' | 'insecure' | 'exists' | 'wrong-host' | 'bad-message' | 'mismatch' | 'no-owner' | 'elsewhere' | 'no-preview' | 'failed'

export class PasskeyError extends Error {
  constructor(
    readonly code: PasskeyErrorCode,
    message: string = code,
  ) {
    super(message)
  }
}

export type Support = 'ok' | 'insecure' | 'unsupported'

export function support(): Support {
  if (!window.isSecureContext) return 'insecure'
  if (typeof PublicKeyCredential === 'undefined' || !navigator.credentials?.create) return 'unsupported'
  return 'ok'
}

const fromBin = (b: ArrayBuffer | Uint8Array) => {
  const u = b instanceof Uint8Array ? b : new Uint8Array(b)
  let s = ''
  for (const c of u) s += String.fromCharCode(c)
  return btoa(s)
}

export const b64url = (b: ArrayBuffer | Uint8Array) => fromBin(b).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')

export function fromB64(s: string): Uint8Array<ArrayBuffer> {
  const std = s.replace(/-/g, '+').replace(/_/g, '/')
  const bin = atob(std + '='.repeat((4 - (std.length % 4)) % 4))
  const out = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
  return out
}

export const toStdB64 = (url: string) => fromBin(fromB64(url))
export const stdB64 = (b: Uint8Array) => fromBin(b)

const utf8 = (s: string) => new TextEncoder().encode(s)

export const toHex = (b: ArrayBuffer | Uint8Array) => [...(b instanceof Uint8Array ? b : new Uint8Array(b))].map((x) => x.toString(16).padStart(2, '0')).join('')

export const sha256Hex = async (b: Uint8Array<ArrayBuffer> | string) => toHex(await crypto.subtle.digest('SHA-256', typeof b === 'string' ? utf8(b) : b))

/** "abcdef012345" as "abcd ef01 2345", easier to compare between two screens. */
export const groupKey = (k: string) => k.match(/.{1,4}/g)?.join(' ') ?? k

/** The server's key fingerprint (keyFingerprint): first 12 hex of SHA-256 of the SubjectPublicKeyInfo DER. */
async function derFingerprint(der: ArrayBuffer | null): Promise<string | undefined> {
  return der ? (await sha256Hex(new Uint8Array(der))).slice(0, 12) : undefined
}

export function pemFingerprint(pem: string): Promise<string | undefined> {
  try {
    return derFingerprint(fromB64(pem.replace(/-----[^-]+-----/g, '').replace(/\s+/g, '')).buffer)
  } catch {
    return Promise.resolve(undefined)
  }
}

/** sign.Subject: sha256 of the params as Go's json.Marshal writes a map (sorted keys, HTML-escaped). */
export function subjectOf(params: Record<string, string>): Promise<string> {
  const sorted = Object.fromEntries(Object.keys(params).sort().map((k) => [k, params[k]]))
  const json = JSON.stringify(sorted).replace(/[<>&\u2028\u2029]/g, (c) => `\\u${c.charCodeAt(0).toString(16).padStart(4, '0')}`)
  return sha256Hex(json)
}

/** sign.ServerIntent.Message, for a server that sends the fields without the bytes. */
export const serverMessage = (i: Record<string, string>) =>
  utf8(`truehand-server-intent/1\n${['action', 'server', 'subject', 'nonce', 'time', 'credential'].map((k) => `${k}: ${i[k] ?? ''}\n`).join('')}`)

/** What a signed message says, read back from its bytes: header line, then "key: value" lines. */
export interface Message {
  header: string
  fields: [string, string][]
  get(key: string): string | undefined
}

const HEADERS = ['truehand-intent/1', 'truehand-server-intent/1']

export function parseMessage(bytes: Uint8Array): Message {
  let text: string
  try {
    text = new TextDecoder('utf-8', { fatal: true }).decode(bytes)
  } catch {
    throw new PasskeyError('bad-message', 'not UTF-8')
  }
  if (!text.endsWith('\n') || text.includes('\r')) throw new PasskeyError('bad-message', 'malformed lines')
  const [header, ...lines] = text.slice(0, -1).split('\n')
  if (!HEADERS.includes(header)) throw new PasskeyError('bad-message', `unknown header ${header}`)
  const fields: [string, string][] = []
  for (const l of lines) {
    const m = /^([a-z0-9_]+): (.*)$/.exec(l)
    if (!m || fields.some(([k]) => k === m[1])) throw new PasskeyError('bad-message', `bad line ${l}`)
    fields.push([m[1], m[2]])
  }
  return { header, fields, get: (k) => fields.find(([x]) => x === k)?.[1] }
}

/** The intent object the server decodes, built from the signed bytes rather than from the server's JSON. */
export const intentOf = (m: Message) => Object.fromEntries(m.fields)

/** Refuses a message that does not say what the person is looking at (the plan on screen, this task). */
export function expect(m: Message, want: Record<string, string | undefined>) {
  for (const [k, v] of Object.entries(want)) if (v !== undefined && m.get(k) !== v) throw new PasskeyError('mismatch', `${k}: ${m.get(k) ?? ''} ≠ ${v}`)
}

function failure(e: unknown, started: number, timeout: number, create: boolean): PasskeyError {
  if (e instanceof PasskeyError) return e
  const name = e instanceof DOMException ? e.name : ''
  const msg = e instanceof Error ? e.message : String(e)
  switch (name) {
    case 'NotAllowedError':
      return new PasskeyError(Date.now() - started >= timeout - 1000 ? 'timeout' : 'cancelled', msg)
    case 'AbortError':
      return new PasskeyError('cancelled', msg)
    case 'InvalidStateError':
      return new PasskeyError(create ? 'exists' : 'failed', msg)
    case 'SecurityError':
      return new PasskeyError(window.isSecureContext ? 'wrong-host' : 'insecure', msg)
    case 'NotSupportedError':
      return new PasskeyError('unsupported', msg)
  }
  return new PasskeyError('failed', msg)
}

function ready() {
  const s = support()
  if (s !== 'ok') throw new PasskeyError(s)
}

interface CredentialDescriptorJSON {
  type: 'public-key'
  id: string
  transports?: string[]
}

/** PublicKeyCredentialCreationOptions as JSON: binary fields in base64url. */
export interface CreationOptionsJSON {
  challenge: string
  rp: { id?: string; name: string }
  user: { id: string; name: string; displayName: string }
  pubKeyCredParams: { type: 'public-key'; alg: number }[]
  timeout?: number
  excludeCredentials?: CredentialDescriptorJSON[]
  authenticatorSelection?: AuthenticatorSelectionCriteria
  attestation?: AttestationConveyancePreference
}

export interface RegistrationJSON {
  id: string
  rawId: string
  type: 'public-key'
  authenticatorAttachment?: string
  response: { clientDataJSON: string; attestationObject: string; transports?: string[] }
  /** The new key's fingerprint as this authenticator reports it, to compare with what the server stored. */
  fingerprint?: string
}

const descriptor = (d: CredentialDescriptorJSON): PublicKeyCredentialDescriptor => ({ type: 'public-key', id: fromB64(d.id), transports: d.transports as AuthenticatorTransport[] | undefined })

export async function register(o: CreationOptionsJSON): Promise<RegistrationJSON> {
  ready()
  const timeout = o.timeout ?? 120_000
  const started = Date.now()
  try {
    const c = (await navigator.credentials.create({
      publicKey: {
        challenge: fromB64(o.challenge),
        rp: o.rp,
        user: { ...o.user, id: fromB64(o.user.id) },
        pubKeyCredParams: o.pubKeyCredParams,
        timeout,
        excludeCredentials: o.excludeCredentials?.map(descriptor),
        authenticatorSelection: o.authenticatorSelection,
        attestation: o.attestation ?? 'none',
      },
    })) as PublicKeyCredential | null
    if (!c) throw new PasskeyError('cancelled')
    const r = c.response as AuthenticatorAttestationResponse
    return {
      id: c.id,
      rawId: b64url(c.rawId),
      type: 'public-key',
      authenticatorAttachment: c.authenticatorAttachment ?? undefined,
      response: { clientDataJSON: b64url(r.clientDataJSON), attestationObject: b64url(r.attestationObject), transports: r.getTransports?.() },
      fingerprint: await derFingerprint(r.getPublicKey?.() ?? null),
    }
  } catch (e) {
    throw failure(e, started, timeout, true)
  }
}

/** A server's request to sign: the message bytes (standard base64), for which relying party and credentials. */
export interface SignRequest {
  message: string
  rpId?: string
  allowCredentials?: string[]
}

export interface Assertion {
  credentialId: string
  authenticatorData: string
  clientDataJSON: string
  /** base64url, as the authenticator returns it. */
  signature: string
}

export const messageBytes = (r: SignRequest) => fromB64(r.message)

/** Signs the message: the challenge is SHA-256 of its bytes, as internal/sign verifies. */
export async function sign(r: SignRequest, timeout = 120_000): Promise<Assertion> {
  ready()
  if (r.rpId && r.rpId !== location.hostname) throw new PasskeyError('wrong-host', r.rpId)
  const started = Date.now()
  try {
    const challenge = await crypto.subtle.digest('SHA-256', messageBytes(r))
    const c = (await navigator.credentials.get({
      publicKey: {
        challenge,
        rpId: r.rpId,
        timeout,
        userVerification: 'required',
        allowCredentials: r.allowCredentials?.map((id) => descriptor({ type: 'public-key', id })),
      },
    })) as PublicKeyCredential | null
    if (!c) throw new PasskeyError('cancelled')
    const a = c.response as AuthenticatorAssertionResponse
    return { credentialId: b64url(c.rawId), authenticatorData: b64url(a.authenticatorData), clientDataJSON: b64url(a.clientDataJSON), signature: b64url(a.signature) }
  } catch (e) {
    throw failure(e, started, timeout, false)
  }
}

const KNOWN = 'truehand.passkeys'

/** Credential ids registered from this browser: only a hint for choosing an approver, never trusted. */
export function known(): string[] {
  try {
    const v = JSON.parse(localStorage.getItem(KNOWN) ?? '[]')
    return Array.isArray(v) ? v.filter((x) => typeof x === 'string') : []
  } catch {
    return []
  }
}

export function remember(id: string) {
  try {
    const ids = known()
    if (!ids.includes(id)) localStorage.setItem(KNOWN, JSON.stringify([...ids, id]))
  } catch {
    // private mode: choosing the approver falls back to asking
  }
}

export function forget(id: string) {
  try {
    localStorage.setItem(KNOWN, JSON.stringify(known().filter((x) => x !== id)))
  } catch {
    // nothing stored
  }
}
