<script lang="ts">
  import Icon, { type IconName } from './Icon.svelte'
  import type { TaskAction, TaskSummary } from '../api'
  import { t, type Key } from '../i18n/index.svelte'
  import { app, isOffline, NEEDS_HUMAN, passkeySigner, queuedFor } from '../stores/app.svelte'
  import { busy, confirmQueued, runAction } from '../stores/actions.svelte'
  import { href } from '../stores/router.svelte'

  function toRestack(e: MouseEvent) {
    e.stopPropagation()
    const el = document.getElementById('restack-title')
    if (!el) return
    e.preventDefault()
    el.scrollIntoView({ block: 'center' })
  }

  let { task, size = 'md', hide = [] }: { task: TaskSummary; size?: 'sm' | 'md'; hide?: TaskAction[] } = $props()

  const AGENT: TaskAction[] = ['start-agent', 'resume-agent', 'start-review']
  // A task others are stacked on is closed to agents; a broken stack is fixed by a restack first.
  const action = $derived(task.tip && task.primaryAction && AGENT.includes(task.primaryAction) ? undefined : task.primaryAction)
  const restackFirst = $derived(!!task.stackError && (!action || AGENT.includes(action)))
  const queued = $derived(queuedFor(task.projectId, task.id)[0])
  const canApprove = $derived(app.data?.me.canApprove ?? false)
  const signs = $derived(canApprove || (!!app.data && passkeySigner()))
  const human = $derived(!!action && NEEDS_HUMAN.includes(action))
  const offline = $derived(isOffline(task.projectId))
  const readOnly = $derived(app.data?.me.readOnly ?? false)
  const loading = $derived(busy.key === `${task.projectId}/${task.id}`)

  const label = $derived.by((): Key => {
    if (!action) return 'action.start-agent'
    if (action === 'open-agent' && (task.waitReason === 'question' || task.waitReason === 'permission')) return 'action.answer-agent'
    if (human && !signs) return `actionRemote.${action as 'approve-plan' | 'push' | 'open-pr'}`
    return `action.${action}`
  })

  const ICONS: Record<TaskAction, IconName> = {
    activate: 'check',
    'start-agent': 'play',
    'resume-agent': 'play',
    'open-agent': 'terminal',
    'approve-plan': 'shield',
    deliver: 'check',
    'start-review': 'eye',
    push: 'upload',
    'open-pr': 'globe',
    'send-back': 'undo',
  }
  const QUIET: TaskAction[] = ['open-agent']
</script>

{#if restackFirst}
  <a class="btn {size === 'sm' ? 'sm' : 'primary'}" href={href({ name: 'task', project: task.projectId, id: task.id, tab: 'description' })} onclick={toRestack}>
    <Icon name="branch" size={size === 'sm' ? 14 : 16} />{t('restack.button')}
  </a>
{:else if queued}
  {#if canApprove}
    <button class="btn {size === 'sm' ? 'sm' : 'primary'}" onclick={(e) => (e.preventDefault(), e.stopPropagation(), confirmQueued(queued.id))}>
      <Icon name="shield" size={size === 'sm' ? 14 : 16} />{t('action.confirm-queued')}
    </button>
  {:else}
    <span class="status queued"><Icon name="clock" size={14} />{t('action.queuedShort')}</span>
  {/if}
{:else if action && !hide.includes(action)}
  <button
    class="btn {size === 'sm' ? 'sm' : ''}"
    class:primary={size === 'md' && !QUIET.includes(action)}
    disabled={loading || offline || readOnly}
    aria-busy={loading}
    title={readOnly ? t('app.readOnly') : offline ? t('machine.offlineHint') : human && !signs ? t('action.remoteHint') : undefined}
    onclick={(e) => {
      e.preventDefault()
      e.stopPropagation()
      runAction(task, action)
    }}
  >
    <Icon name={ICONS[action]} size={size === 'sm' ? 14 : 16} />{t(label)}
  </button>
{:else if task.merged && size === 'md'}
  <span class="status ok"><Icon name="check" size={14} />{t('state.merged')}</span>
{:else if task.prUrl && size === 'md'}
  <a class="status link" href={task.prUrl} target="_blank" rel="noreferrer"><Icon name="globe" size={14} />{t('task.prOpen')}</a>
{/if}

<style>
  .status {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 13px;
    font-weight: 600;
    white-space: nowrap;
  }
  .ok {
    color: var(--ok);
  }
  .queued,
  .link {
    color: var(--info);
  }
</style>
