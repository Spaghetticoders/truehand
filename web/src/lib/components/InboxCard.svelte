<script lang="ts">
  import Icon, { type IconName } from './Icon.svelte'
  import Reveal from './Reveal.svelte'
  import type { InboxItem, InboxKind } from '../stores/inbox.svelte'
  import { t, timeAgo } from '../i18n/index.svelte'
  import { project, sessionsOf } from '../stores/app.svelte'
  import { api } from '../api'
  import { planBriefText } from '../plan'
  import { limited } from '../limit'
  import { outsideScope } from '../stores/scope.svelte'
  import { sessionLabel } from '../actors'

  let { item, onopen }: { item: InboxItem; onopen: (i: InboxItem) => void } = $props()

  const ICON: Record<InboxKind, IconName> = {
    plan: 'shield',
    permission: 'lock',
    question: 'question',
    restack: 'branch',
    scope: 'folder',
    review: 'eye',
    deliver: 'check',
    push: 'upload',
    pr: 'globe',
    config: 'shield',
    queued: 'clock',
  }
  const TONE: Record<InboxKind, string> = {
    plan: 'info',
    permission: 'warn',
    question: 'warn',
    restack: 'err',
    scope: 'warn',
    review: 'ok',
    deliver: 'ok',
    push: 'ok',
    pr: 'ok',
    config: 'info',
    queued: 'idle',
  }
  // What the agent is asking for, from its last line: more useful than the session's name.
  const waiting = $derived(item.session ?? (item.task ? sessionsOf(item.task.projectId, item.task.id).find((x) => x.status === 'waiting') : undefined))
  const asks = $derived(item.kind === 'permission' || item.kind === 'question' ? (waiting?.tail.filter((l) => l.trim()).at(-1) ?? '') : '')

  let brief = $state('')
  $effect(() => {
    const task = item.task
    if (item.kind !== 'plan' || !task) return
    void task.updatedAt
    limited(() => api.task(task.projectId, task.id))
      .then((d) => (brief = planBriefText(d.plan ?? '')))
      .catch(() => (brief = ''))
  })

  const title = $derived(item.task?.title ?? (item.session ? (asks || sessionLabel(item.session.role)) : t(`queuedKind.${item.queued?.kind === 'commit-config' ? 'commit-config' : 'config'}`)))
</script>

<li class="card">
  <button class="hit" onclick={() => onopen(item)} aria-label={`${t(`inbox.kind.${item.kind}`)}: ${title}`}>
    <span class="kinds">
      <span class="kind {TONE[item.kind]}"><Icon name={ICON[item.kind]} size={14} />{t(`inbox.kind.${item.kind}`)}</span>
      {#if item.also}<span class="kind {TONE[item.also]}"><Icon name={ICON[item.also]} size={14} />{t(`inbox.kind.${item.also}`)}</span>{/if}
    </span>
    <span class="title">{#if item.session && asks}<Reveal text={asks} />{:else}{title}{/if}</span>
    {#if brief}<span class="brief">{brief}</span>{/if}
    {#if item.kind === 'scope' && item.task}<span class="brief">{t('scope.cardLine', { n: outsideScope[`${item.task.projectId}/${item.task.id}`] ?? 0 })}</span>{/if}
    {#if asks && item.task}<span class="asks"><span class="clamp"><Reveal text={asks} /></span></span>{/if}
    <span class="foot">
      <span class="meta">{item.task ? `${item.task.id} · ` : item.session ? `${sessionLabel(item.session.role)} · ` : ''}{project(item.projectId)?.name ?? ''} · {timeAgo(item.since)}{#if item.queued && item.task} · {t('wait.from', { device: item.queued.device })}{/if}</span>
      <span class="go" aria-hidden="true">{t('inbox.open')}<Icon name="chevron-right" size={16} /></span>
    </span>
  </button>
</li>

<style>
  .card {
    list-style: none;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
  }
  .hit {
    width: 100%;
    display: grid;
    gap: 6px;
    padding: 14px 14px 12px;
    border: 0;
    background: none;
    color: inherit;
    text-align: left;
    font: inherit;
    cursor: pointer;
  }
  .kinds {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .kind {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    justify-self: start;
    padding: 2px 8px;
    border-radius: 10px;
    font-size: 12px;
    font-weight: 600;
  }
  .kind.info {
    background: var(--info-bg);
    color: var(--info);
  }
  .kind.warn {
    background: var(--warn-bg);
    color: var(--warn);
  }
  .kind.err {
    background: var(--err-bg);
    color: var(--err);
  }
  .kind.ok {
    background: var(--ok-bg);
    color: var(--ok);
  }
  .kind.idle {
    background: var(--idle-bg);
    color: var(--idle);
  }
  .title {
    font-size: 16px;
    font-weight: 600;
    line-height: 1.3;
  }
  .meta {
    font-size: 12.5px;
    color: var(--text-3);
    overflow-wrap: anywhere;
  }
  .asks {
    color: var(--text-2);
  }
  .brief,
  .asks .clamp {
    font-size: 13.5px;
    color: var(--text-2);
    display: -webkit-box;
    -webkit-box-orient: vertical;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    overflow: hidden;
    overflow-wrap: anywhere;
  }
  .asks,
  .asks .clamp {
    font-family: var(--mono);
    font-size: 12.5px;
  }
  .asks {
    padding: 6px 8px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
  }
  .foot {
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .foot .meta {
    flex: 1;
    min-width: 0;
  }
  .go {
    flex: none;
    min-height: 44px;
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 0 14px;
    border-radius: var(--radius);
    background: var(--accent);
    color: var(--accent-fg);
    font-size: 14px;
    font-weight: 600;
  }
</style>
