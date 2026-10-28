import { api, ApiError, type ActionArgs, type ActResult, type TaskAction, type TaskSummary } from '../api'
import { t } from '../i18n/index.svelte'
import { app, noteQueued, sessionsOf, isLive } from './app.svelte'
import { go } from './router.svelte'
import { ui } from './ui.svelte'

export interface Toast {
  id: number
  kind: 'ok' | 'error' | 'queued'
  text: string
  href?: string
}

export const toasts = $state<{ list: Toast[] }>({ list: [] })
let seq = 0

export function toast(kind: Toast['kind'], text: string, href?: string) {
  const id = ++seq
  toasts.list.push({ id, kind, text, href })
  setTimeout(() => (toasts.list = toasts.list.filter((x) => x.id !== id)), kind === 'ok' ? 4500 : 8000)
}

const CODED = ['already-merged', 'no-remote', 'remote-not-github', 'gh-missing', 'config-not-committed', 'config-not-pushed', 'no-approvers', 'config-changed', 'nothing-to-commit', 'other-changes-staged', 'machine-disabled'] as const

export function errorText(e: unknown): string {
  if (e instanceof ApiError) {
    const code = e.code as (typeof CODED)[number] | undefined
    if (code && CODED.includes(code)) return t(`err.${code}`)
    if (e.code === 'remote-approval-unavailable') return t('action.remoteUnavailable')
    if (e.code === 'machine-offline' || e.code === 'machine-unreachable') return t('machine.unreachable', { error: e.message })
    if (e.code === 'signature-required') return t('action.signatureRequired')
    if (e.code === 'remote-unavailable') return t('err.remote-unavailable', { error: e.message.replace(/^saved, but remote access could not start: /, '') })
    if (e.code === 'config-invalid') return t('err.config-invalid', { error: e.message })
    if (e.code === 'not-on-base') return t('err.not-on-base', { base: /check out (\S+) first/.exec(e.message)?.[1] ?? '' })
    const gh = (e.body as { reason?: string } | undefined)?.reason ?? /^gh pr create failed(?: on the Mac)?: (.*)$/s.exec(e.message)?.[1]
    if (e.code === 'gh-failed' || gh !== undefined) return ghText(gh ?? e.message)
    if (e.status === 403) return t('action.forbidden', { error: e.message })
    return e.message
  }
  if (typeof e === 'string' && /cancel/i.test(e)) return t('action.signCancelled')
  return e instanceof Error ? e.message : String(e)
}

const ghText = (reason: string) => (/gh auth login|not logged in|authentication/i.test(reason) ? t('err.gh-auth') : t('err.gh-failed', { reason }))

/** Runs a human-factor action; if the repo wants a signature, asks the Mac app for it and retries once. */
async function withSignature(pid: string, id: string, action: TaskAction, args: ActionArgs) {
  try {
    return checked(await api.act(pid, id, action, args))
  } catch (e) {
    const signedIntent = await signFor(e, pid, id, action)
    return checked(await api.act(pid, id, action, { ...args, signedIntent }))
  }
}

/** On a 409 signature-required, gets the Mac app's Touch ID signature; rethrows anything else. */
async function signFor(e: unknown, pid: string, id: string, action: TaskAction) {
  if (!(e instanceof ApiError && e.code === 'signature-required')) throw e
  const approvers = (e.body as { approvers?: string[] } | undefined)?.approvers ?? []
  // With one approver the id is known; with several the shell signs with its own key.
  const signing = api.signIntent(pid, id, action, approvers.length === 1 ? approvers[0] : undefined)
  if (!signing) throw e
  return signing
}

/** A 200 can still carry a failure (an approval recorded but a move refused): surface it. */
function checked(r: ActResult): ActResult {
  if (!r.ok && !r.queued && r.error) throw new Error(r.error)
  return r
}

export const busy = $state<{ key: string | null }>({ key: null })

export async function runAction(task: TaskSummary, action: TaskAction) {
  const { projectId: pid, id } = task
  const agentTab = (session?: string) => go({ name: 'task', project: pid, id, tab: 'agent', session })
  if (action === 'activate') return go({ name: 'activate', project: pid })
  if (action === 'send-back') return void (ui.sendBack = { pid, id })
  if (action === 'open-agent') {
    const ss = sessionsOf(pid, id)
    return agentTab((ss.find((s) => s.status === 'waiting') ?? ss.find(isLive))?.id)
  }
  busy.key = `${pid}/${id}`
  try {
    switch (action) {
      case 'start-agent':
      case 'start-review':
      case 'resume-agent': {
        const r = checked(await api.act(pid, id, action))
        return agentTab(r.sessionId)
      }
      case 'approve-plan': {
        const d = await api.task(pid, id)
        const r = await withSignature(pid, id, action, { planSha256: d.planSha256 })
        if (r.queued && r.queuedId) noteQueued({ id: r.queuedId, kind: 'approve', projectId: pid, taskId: id, planSha256: d.planSha256, device: t('queuedKind.thisDevice'), time: new Date().toISOString() })
        return toast(r.queued ? 'queued' : 'ok', t(r.queued ? 'action.queued' : 'action.planApproved'))
      }
      case 'deliver':
        checked(await api.act(pid, id, action))
        return toast('ok', t('action.delivered'))
      case 'push':
      case 'open-pr': {
        const r = await withSignature(pid, id, action, {})
        if (r.queued) {
          if (r.queuedId) noteQueued({ id: r.queuedId, kind: action === 'push' ? 'push' : 'pr', projectId: pid, taskId: id, device: t('queuedKind.thisDevice'), time: new Date().toISOString() })
          return toast('queued', t('action.queued'))
        }
        return toast('ok', t(action === 'push' ? 'action.pushed' : 'action.prOpened'), r.url)
      }
    }
  } catch (e) {
    toast('error', errorText(e))
  } finally {
    busy.key = null
  }
}

const QUEUED_ACTION = { approve: 'approve-plan', push: 'push', pr: 'open-pr' } as const

export async function confirmQueued(qid: string) {
  try {
    let r: ActResult
    try {
      r = checked(await api.confirmQueued(qid))
    } catch (e) {
      const q = app.data?.queued.find((x) => x.id === qid)
      if (!q || q.kind === 'config' || q.kind === 'commit-config') throw e
      r = checked(await api.confirmQueued(qid, await signFor(e, q.projectId, q.taskId, QUEUED_ACTION[q.kind])))
    }
    toast('ok', t('action.confirmed'), r.url)
  } catch (e) {
    toast('error', errorText(e))
  }
}

export async function discardQueued(qid: string) {
  try {
    await api.discardQueued(qid)
  } catch (e) {
    toast('error', errorText(e))
  }
}
