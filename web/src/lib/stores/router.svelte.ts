export type TaskTab = 'description' | 'agent' | 'plan' | 'proofs' | 'diff'

export const TASK_TABS: TaskTab[] = ['description', 'agent', 'plan', 'proofs', 'diff']

export type Route =
  | { name: 'board'; project?: string; bucket?: string }
  | { name: 'task'; project: string; id: string; tab: TaskTab; session?: string }
  | { name: 'sessions' }
  | { name: 'settings' }
  | { name: 'activate'; project: string }
  | { name: 'pair'; code?: string }

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
    default:
      return { name: 'board', project: q.get('project') ?? undefined, bucket: q.get('bucket') ?? undefined }
  }
}

export function href(r: Route): string {
  switch (r.name) {
    case 'task': {
      const q = new URLSearchParams()
      if (r.tab !== 'description') q.set('tab', r.tab)
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
    case 'board': {
      const q = new URLSearchParams()
      if (r.project) q.set('project', r.project)
      if (r.bucket) q.set('bucket', r.bucket)
      const qs = q.toString()
      return `#/${qs ? `?${qs}` : ''}`
    }
  }
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
