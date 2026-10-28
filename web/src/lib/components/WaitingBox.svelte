<script lang="ts">
  import Icon, { type IconName } from './Icon.svelte'
  import ActionButton from './ActionButton.svelte'
  import Dot from './Dot.svelte'
  import type { QueuedAction, Session, TaskSummary, WaitReason } from '../api'
  import { t, timeAgo } from '../i18n/index.svelte'
  import { app, BUCKET_OF, BUCKETS, project, queuedFor, reasonKey, reasonsOf, type Bucket } from '../stores/app.svelte'
  import { go, href } from '../stores/router.svelte'
  import { sessionLabel } from '../actors'
  import { confirmQueued } from '../stores/actions.svelte'

  let { filter, bucket }: { filter?: string; bucket?: Bucket } = $props()

  const ICON: Record<Bucket, IconName> = { approvals: 'shield', questions: 'question', deliver: 'upload', blocked: 'alert' }

  type Chip = WaitReason | 'queued'
  type Item = { key: string; pid: string; since: string; chips: Chip[]; task?: TaskSummary; session?: Session; device?: string; config?: QueuedAction }

  const items = $derived.by(() => {
    const out: Item[] = []
    for (const task of app.data?.tasks ?? []) {
      if (task.readOnly || (filter && task.projectId !== filter)) continue
      const q = queuedFor(task.projectId, task.id)[0]
      const chips: Chip[] = [...(q ? (['queued'] as Chip[]) : []), ...reasonsOf(task)]
      if (chips.length) out.push({ key: `${task.projectId}/${task.id}`, pid: task.projectId, since: q?.time ?? task.updatedAt, chips, task, device: q?.device })
    }
    for (const s of app.data?.sessions ?? []) {
      if (s.taskId || s.status !== 'waiting' || (filter && s.projectId !== filter)) continue
      out.push({ key: s.id, pid: s.projectId, since: s.startedAt, chips: [s.waitingFor === 'permission' ? 'permission' : 'question'], session: s })
    }
    for (const q of app.data?.queued ?? []) {
      if ((q.kind !== 'config' && q.kind !== 'commit-config') || (filter && q.projectId !== filter)) continue
      out.push({ key: q.id, pid: q.projectId, since: q.time, chips: ['queued'], device: q.device, config: q })
    }
    return out.sort((a, b) => b.since.localeCompare(a.since))
  })
  const bucketOf = (i: Item) => BUCKET_OF[i.chips[0]]
  const counts = $derived(Object.fromEntries(BUCKETS.map((b) => [b, items.filter((i) => bucketOf(i) === b).length])) as Record<Bucket, number>)
  const shown = $derived(bucket ? items.filter((i) => bucketOf(i) === bucket) : items)

  const setBucket = (b: Bucket) => go({ name: 'board', project: filter, bucket: bucket === b ? undefined : b })
</script>

<section class="inbox card" aria-labelledby="inbox-title">
  <div class="inbox-head">
    <h2 id="inbox-title">{t('board.waiting')}</h2>
    <span class="count">{items.length}</span>
  </div>
  <div class="buckets">
    {#each BUCKETS as b (b)}
      <button class="bucket {b}" class:zero={!counts[b]} aria-pressed={bucket === b} onclick={() => setBucket(b)}>
        <span class="b-icon"><Icon name={ICON[b]} /></span>
        <span class="b-num">{counts[b]}</span>
        <span class="b-label">{t(`bucket.${b}`)}</span>
      </button>
    {/each}
  </div>
  {#if shown.length}
    <ul class="list">
      {#each shown as i (i.key)}
        <li>
          <a class="row" href={i.task ? href({ name: 'task', project: i.pid, id: i.task.id, tab: 'description' }) : i.config ? href({ name: 'activate', project: i.pid }) : href({ name: 'sessions' })}>
            <span class="chips">
              {#each i.chips as c (c)}<span class="reason {BUCKET_OF[c]}">{t(reasonKey(i.task, c))}</span>{/each}
            </span>
            <span class="id mono">{i.task?.id ?? ''}</span>
            <span class="title">
              {#if i.task}{i.task.title}{:else if i.config}{t(`queuedKind.${i.config.kind === 'commit-config' ? 'commit-config' : 'config'}`)}{:else if i.session}<Dot status="waiting" />{sessionLabel(i.session.role)}{/if}
              {#if i.device}<span class="via">{t('wait.from', { device: i.device })}</span>{/if}
            </span>
            <span class="meta">{project(i.pid)?.name} · {timeAgo(i.since)}</span>
          </a>
          {#if i.task}
            <ActionButton task={i.task} size="sm" />
          {:else if i.config?.kind === 'commit-config'}
            <a class="btn sm" href={href({ name: 'activate', project: i.pid })}><Icon name="shield" size={14} />{t('action.review-queued')}</a>
          {:else if i.config}
            {#if app.data?.me.canApprove}
              <button class="btn sm" onclick={() => confirmQueued(i.config!.id)}><Icon name="shield" size={14} />{t('action.confirm-queued')}</button>
            {:else}
              <span class="queued-note"><Icon name="clock" size={14} />{t('action.queuedShort')}</span>
            {/if}
          {:else}
            <a class="btn sm" href={href({ name: 'sessions' })}>{t('action.answer-agent')}</a>
          {/if}
        </li>
      {/each}
    </ul>
  {:else}
    <p class="empty">{t('board.waitingEmpty')}</p>
  {/if}
</section>

<style>
  .inbox {
    padding: 16px;
    display: grid;
    gap: 14px;
    border-color: var(--border-strong);
  }
  .inbox-head {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  h2 {
    font-size: 15px;
  }
  .count {
    min-width: 22px;
    height: 20px;
    padding: 0 6px;
    border-radius: 10px;
    background: var(--warn-bg);
    color: var(--warn);
    font-size: 12px;
    font-weight: 700;
    display: grid;
    place-items: center;
  }
  .buckets {
    display: grid;
    grid-template-columns: repeat(4, 1fr);
    gap: 8px;
  }
  .bucket {
    display: grid;
    grid-template-columns: auto 1fr;
    grid-template-rows: auto auto;
    column-gap: 10px;
    align-items: center;
    text-align: left;
    padding: 10px 12px;
    border-radius: var(--radius);
    border: 1px solid var(--border);
    background: var(--surface-2);
    cursor: pointer;
  }
  .bucket:hover {
    border-color: var(--border-strong);
  }
  .bucket[aria-pressed='true'] {
    border-color: var(--accent);
    box-shadow: inset 0 0 0 1px var(--accent);
  }
  .b-icon {
    grid-row: span 2;
    display: grid;
    place-items: center;
    width: 30px;
    height: 30px;
    border-radius: 8px;
  }
  .b-num {
    font-size: 18px;
    font-weight: 700;
    line-height: 1.1;
  }
  .b-label {
    font-size: 12px;
    color: var(--text-2);
  }
  .approvals .b-icon,
  .reason.approvals {
    background: var(--info-bg);
    color: var(--info);
  }
  .questions .b-icon,
  .reason.questions {
    background: var(--warn-bg);
    color: var(--warn);
  }
  .deliver .b-icon,
  .reason.deliver {
    background: var(--ok-bg);
    color: var(--ok);
  }
  .blocked .b-icon,
  .reason.blocked {
    background: var(--err-bg);
    color: var(--err);
  }
  .bucket.zero .b-icon {
    background: var(--idle-bg);
    color: var(--text-3);
  }
  .bucket.zero .b-num {
    color: var(--text-3);
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
    border-top: 1px solid var(--border);
  }
  .list li {
    display: flex;
    align-items: center;
    gap: 12px;
    border-bottom: 1px solid var(--border);
  }
  .list li:last-child {
    border-bottom: 0;
  }
  .row {
    flex: 1;
    min-width: 0;
    display: grid;
    grid-template-columns: 200px 74px minmax(0, 1fr) auto;
    align-items: center;
    gap: 10px;
    padding: 9px 4px;
  }
  .row:hover .title {
    text-decoration: underline;
    text-underline-offset: 2px;
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    justify-self: start;
  }
  .reason {
    display: inline-flex;
    align-items: center;
    height: 20px;
    padding: 0 7px;
    border-radius: 10px;
    font-size: 11.5px;
    font-weight: 600;
    white-space: nowrap;
  }
  .id {
    font-size: 12px;
    color: var(--text-2);
    white-space: nowrap;
  }
  .title {
    min-width: 0;
    display: flex;
    align-items: center;
    gap: 8px;
    font-weight: 500;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .via {
    font-size: 12px;
    font-weight: 400;
    color: var(--text-3);
  }
  .queued-note {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 12.5px;
    font-weight: 600;
    color: var(--info);
    white-space: nowrap;
  }
  .meta {
    font-size: 12px;
    color: var(--text-3);
    white-space: nowrap;
  }
  .empty {
    color: var(--text-2);
    padding: 4px 2px;
  }
  @media (max-width: 900px) {
    .buckets {
      grid-template-columns: repeat(2, 1fr);
    }
    .row {
      grid-template-columns: auto minmax(0, 1fr);
      grid-template-areas: 'reason reason' 'id title' 'meta meta';
      row-gap: 3px;
    }
    .chips {
      grid-area: reason;
    }
    .id {
      grid-area: id;
    }
    .title {
      grid-area: title;
      white-space: normal;
      flex-wrap: wrap;
    }
    .meta {
      grid-area: meta;
    }
  }
  @media (max-width: 640px) {
    .inbox {
      padding: 12px;
    }
    .bucket {
      padding: 8px 10px;
    }
  }
</style>
