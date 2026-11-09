export type TaskTab = 'description' | 'agent' | 'plan' | 'proofs' | 'diff'

export const TASK_TABS: TaskTab[] = ['description', 'agent', 'plan', 'proofs', 'diff']

export type Route =
  | { name: 'board'; project?: string; bucket?: string }
  /** tabSet: the link named a tab; otherwise the page may pick the one that matters (the plan to approve). */
  | { name: 'task'; project: string; id: string; tab: TaskTab; session?: string; tabSet?: boolean }
  | { name: 'sessions' }
  | { name: 'settings' }
  | { name: 'activate'; project: string }
  | { name: 'pair'; code?: string }
  | { name: 'enroll'; code?: string }
  | { name: 'agents' }
  /** One session, full screen on the phone. */
  | { name: 'session'; id: string }
  | { name: 'projects' }

function parse(hash: string): Route {
  const [path, query = ''] = hash.replace(/^#\/?/, '').split('?')
  const q = new URLSearchParams(query)
  const parts = path.split('/').filter(Boolean).map(decodeURIComponent)
  switch (parts[0]) {
    case 'task': {
      const tab = (q.get('tab') ?? 'description') as TaskTab
      return {
        name: 'task',
        project: parts[1] ?? '',
        id: parts[2] ?? '',
        tab: TASK_TABS.includes(tab) ? tab : 'description',
        session: q.get('session') ?? undefined,
        tabSet: q.has('tab'),
      }
    }
    case 'sessions':
      return { name: 'sessions' }
    case 'settings':
      return { name: 'settings' }
    case 'activate':
      return { name: 'activate', project: parts[1] ?? '' }
    case 'pair':
      return { name: 'pair', code: q.get('code') ?? undefined }
    case 'enroll':
      return { name: 'enroll', code: q.get('code') ?? undefined }
    case 'agents':
      return { name: 'agents' }
    case 'session':
      return parts[1] ? { name: 'session', id: parts[1] } : { name: 'agents' }
    case 'projects':
      return { name: 'projects' }
    default:
      return { name: 'board', project: q.get('project') ?? undefined, bucket: q.get('bucket') ?? undefined }
  }
}

export function href(r: Route): string {
  switch (r.name) {
    case 'task': {
      const q = new URLSearchParams()
      if (r.tab !== 'description' || r.tabSet) q.set('tab', r.tab)
      if (r.session) q.set('session', r.session)
      const qs = q.toString()
      return `#/task/${encodeURIComponent(r.project)}/${encodeURIComponent(r.id)}${qs ? `?${qs}` : ''}`
    }
    case 'sessions':
      return '#/sessions'
    case 'settings':
      return '#/settings'
    case 'activate':
      return `#/activate/${encodeURIComponent(r.project)}`
    case 'pair':
      return r.code ? `#/pair?code=${encodeURIComponent(r.code)}` : '#/pair'
    case 'enroll':
      return r.code ? `#/enroll?code=${encodeURIComponent(r.code)}` : '#/enroll'
    case 'agents':
      return '#/agents'
    case 'session':
      return `#/session/${encodeURIComponent(r.id)}`
    case 'projects':
      return '#/projects'
    case 'board': {
      const q = new URLSearchParams()
      if (r.project) q.set('project', r.project)
      if (r.bucket) q.set('bucket', r.bucket)
      const qs = q.toString()
      return `#/${qs ? `?${qs}` : ''}`
    }
  }
}

// `truehand passkey bootstrap` prints https://<host>/enroll: a path, not a hash route.
if (!location.hash && /\/enroll\/?$/.test(location.pathname)) {
  const code = new URLSearchParams(location.search).get('code')
  history.replaceState(null, '', `${location.pathname.replace(/enroll\/?$/, '')}${href({ name: 'enroll', code: code ?? undefined })}`)
}

export const router = $state<{ route: Route }>({ route: parse(location.hash) })

export function go(r: Route) {
  location.hash = href(r)
}

export function startRouter() {
  const onHash = () => {
    router.route = parse(location.hash)
  }
  window.addEventListener('hashchange', onHash)
  return () => window.removeEventListener('hashchange', onHash)
}
