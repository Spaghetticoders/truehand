import type { Actor, SessionRole } from './api'
import { t } from './i18n/index.svelte'

export const sessionLabel = (role: SessionRole) => t('actor.agent', { role: t(`role.${role}`) })

export function actorLabel(a: Actor): string {
  switch (a.kind) {
    case 'human':
      return a.name
    case 'agent-dev':
      return sessionLabel('dev')
    case 'agent-review':
      return sessionLabel('review')
    case 'agent-plan':
      return sessionLabel('plan')
    default:
      return t('actor.unbound')
  }
}
