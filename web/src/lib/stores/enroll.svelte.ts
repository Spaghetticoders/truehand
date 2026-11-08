import { api, type Passkey, type PasskeyOptions } from '../api'
import { register, remember } from '../passkeys'

/** Survives the page switching from the pairing layout to the app once the browser is paired. */
export const enrolled = $state<{ passkey: Passkey | null; fingerprint?: string }>({ passkey: null })

/** Asks the server for a registration first: the browser wants the create() call right on a tap, not after a fetch. */
export const prepare = (label: string, code: string) => api.passkeyOptions({ label, code })

export async function finish(o: PasskeyOptions): Promise<Passkey> {
  const c = await register(o.publicKey)
  const pk = await api.registerPasskey({ ceremony: o.ceremony, attestationObject: c.response.attestationObject, clientDataJSON: c.response.clientDataJSON, transports: c.response.transports })
  remember(pk.id)
  enrolled.passkey = pk
  enrolled.fingerprint = c.fingerprint
  return pk
}

export function defaultLabel(): string {
  const ua = navigator.userAgent
  const m = /\(Linux; Android [^;]+; ([^;)]+)/.exec(ua)
  if (m && m[1] !== 'K') return m[1].trim()
  if (/iPhone/.test(ua)) return 'iPhone'
  if (/iPad/.test(ua)) return 'iPad'
  if (/Android/.test(ua)) return 'Android'
  if (/Macintosh/.test(ua)) return 'Mac'
  if (/Windows/.test(ua)) return 'Windows'
  if (/Linux/.test(ua)) return 'Linux'
  return ''
}
