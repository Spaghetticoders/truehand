<script lang="ts">
  import Icon from '../components/Icon.svelte'
  import Dot from '../components/Dot.svelte'
  import StatusChip from '../components/StatusChip.svelte'
  import WaitingBox from '../components/WaitingBox.svelte'
  import { api, TASK_STATES, type TaskSummary } from '../api'
  import { t, timeAgo } from '../i18n/index.svelte'
  import { app, BUCKET_OF, clientName, reasonKey, dotOfAll, isOffline, machineOf, queuedFor, reasonsOf, sessionsOf, type Bucket } from '../stores/app.svelte'
  import { go, href, router } from '../stores/router.svelte'
  import { errorText, toast } from '../stores/actions.svelte'
  import { openNewTask } from '../stores/ui.svelte'

  const route = $derived(router.route.name === 'board' ? router.route : { name: 'board' as const, project: undefined, bucket: undefined })
  const filter = $derived(route.project)
  const bucket = $derived(route.bucket as Bucket | undefined)
  const projects = $derived((app.data?.projects ?? []).filter((p) => !filter || p.id === filter))
  let fetching = $state<string | null>(null)

  const byLifecycle = (a: TaskSummary, b: TaskSummary) => TASK_STATES.indexOf(a.state) - TASK_STATES.indexOf(b.state) || b.updatedAt.localeCompare(a.updatedAt)
  const tasksOf = (pid: string) => app.data?.tasks.filter((x) => x.projectId === pid) ?? []
  const open = (pid: string) => tasksOf(pid).filter((x) => !x.merged).sort(byLifecycle)
  const done = (pid: string) => tasksOf(pid).filter((x) => x.merged).sort((a, b) => b.updatedAt.localeCompare(a.updatedAt))
  const taskHref = (x: TaskSummary) => href({ name: 'task', project: x.projectId, id: x.id, tab: 'description' })

  async function refetch(pid: string) {
    fetching = pid
    try {
      await api.fetchRemote(pid)
      toast('ok', t('board.fetched'))
    } catch (e) {
      toast('error', errorText(e))
    } finally {
      fetching = null
    }
  }
</script>

{#snippet row(task: TaskSummary)}
  {@const dot = dotOfAll(sessionsOf(task.projectId, task.id))}
  <li class:merged={task.merged}>
    <a class="row" href={taskHref(task)}>
      <span class="id mono">{task.id}</span>
      <span class="title">{task.title}</span>
      <span class="tags">
        {#if task.readOnly && task.branch}<span class="tag"><Icon name="branch" size={12} />{task.branch}</span>{/if}
        {#if queuedFor(task.projectId, task.id).length}<span class="tag approvals">{t('wait.queued')}</span>{/if}
        {#each reasonsOf(task) as r (r)}<span class="tag {BUCKET_OF[r]}">{t(reasonKey(task, r))}</span>{/each}
      </span>
      <span class="state"><StatusChip state={task.state} merged={task.merged} /></span>
      <span class="right">
        {#if dot}<Dot status={dot} />{/if}
        <span class="time">{timeAgo(task.updatedAt)}</span>
      </span>
    </a>
  </li>
{/snippet}

<div class="board">
  <header class="head">
    <h1>{t('board.title')}</h1>
    <div class="filters" role="group" aria-label={t('board.filterProjects')}>
      <button class="pill" aria-pressed={!filter} onclick={() => go({ name: 'board', bucket })}>{t('board.all')}</button>
      {#each app.data?.projects ?? [] as p (p.id)}
        <button class="pill" aria-pressed={filter === p.id} onclick={() => go({ name: 'board', project: p.id, bucket })}>{p.name}</button>
      {/each}
    </div>
  </header>

  <WaitingBox {filter} {bucket} />

  {#each projects as p (p.id)}
    {@const list = open(p.id)}
    {@const fatti = done(p.id)}
    {@const m = machineOf(p.id)}
    {@const offline = isOffline(p.id)}
    <section class="project" class:offline aria-labelledby="p-{p.id}">
      <header class="p-head">
        <h2 id="p-{p.id}">
          <span class="client">{clientName(p.clientId)}</span>
          <span class="sep" aria-hidden="true">/</span>
          {p.name}
        </h2>
        {#if m && m.id !== 'local'}<span class="machine"><Icon name="machine" size={11} />{m.label}{#if offline} · {t(`machine.status.${m.status}`)}{/if}</span>{/if}
        <span class="n">{t('board.tasks', { n: list.length + fatti.length })}</span>
        <span class="tools">
          {#if p.active && !offline}
            <button class="btn ghost icon sm" aria-label={t('board.fetch')} title={t('board.fetch')} disabled={fetching === p.id} onclick={() => refetch(p.id)}>
              <Icon name="refresh" size={15} />
            </button>
            <button class="btn ghost icon sm" aria-label="{t('nav.newTask')} · {p.name}" title={t('nav.newTask')} onclick={() => openNewTask(p.id)}><Icon name="plus" /></button>
          {/if}
        </span>
      </header>
      {#if p.configError}
        <div class="card banner err">
          <Icon name="alert" />
          <p><strong>{t('board.configErrorTitle')}</strong> <code>{p.configError}</code></p>
          <a class="btn sm" href={href({ name: 'activate', project: p.id })}>{t('board.fixConfig')}</a>
        </div>
      {:else if !p.active}
        <div class="card banner">
          <Icon name="alert" />
          <p>{t('board.inactiveHint')}</p>
          <a class="btn primary sm" href={href({ name: 'activate', project: p.id })}>{t('action.activate')}</a>
        </div>
      {/if}
      {#if offline}
        <p class="offline-note"><Icon name="alert" size={14} />{t('machine.offlineHint')}</p>
      {/if}
      {#if list.length}
        <ul class="list card">
          {#each list as task (task.id)}{@render row(task)}{/each}
        </ul>
      {:else if p.active}
        <p class="empty card">{t('board.empty')}</p>
      {/if}
      {#if fatti.length}
        <details class="done">
          <summary>{t('board.done', { n: fatti.length })}</summary>
          <ul class="list card">
            {#each fatti as task (task.id)}{@render row(task)}{/each}
          </ul>
        </details>
      {/if}
    </section>
  {/each}
</div>

<style>
  .board {
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    gap: 22px;
    max-width: 1080px;
    margin: 0 auto;
    padding: 22px 28px 40px;
  }
  .head {
    display: flex;
    align-items: center;
    gap: 16px;
    flex-wrap: wrap;
    min-width: 0;
  }
  h1 {
    font-size: 20px;
  }
  .filters {
    display: flex;
    gap: 6px;
    flex-wrap: wrap;
    min-width: 0;
    max-width: 100%;
  }
  .pill {
    height: 28px;
    padding: 0 11px;
    border-radius: 14px;
    border: 1px solid var(--border);
    background: var(--surface);
    color: var(--text-2);
    font-size: 12.5px;
    font-weight: 500;
    cursor: pointer;
    white-space: nowrap;
  }
  .pill[aria-pressed='true'] {
    background: var(--accent);
    border-color: var(--accent);
    color: var(--accent-fg);
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .project {
    display: grid;
    gap: 8px;
  }
  .p-head {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px 10px;
    min-height: 28px;
  }
  .p-head h2 {
    font-size: 14px;
    display: flex;
    gap: 6px;
    align-items: baseline;
    white-space: nowrap;
  }
  .client,
  .sep {
    color: var(--text-3);
    font-weight: 500;
  }
  .n {
    font-size: 12px;
    color: var(--text-3);
    white-space: nowrap;
  }
  .tools {
    margin-left: auto;
    display: flex;
    gap: 2px;
    color: var(--text-2);
  }
  .banner {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 12px 14px;
    border-color: var(--warn);
    background: var(--warn-bg);
    color: var(--warn);
  }
  .banner.err {
    border-color: var(--err);
    background: var(--err-bg);
    color: var(--err);
  }
  .banner p {
    flex: 1;
    min-width: 0;
    color: var(--text);
    overflow-wrap: anywhere;
  }
  .banner code {
    font-size: 12px;
    color: var(--text-2);
  }
  .machine {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 1px 7px;
    border-radius: 10px;
    border: 1px solid var(--border-strong);
    font-size: 11.5px;
    font-weight: 600;
    color: var(--text-2);
    white-space: nowrap;
  }
  .offline .machine {
    border-style: dashed;
    color: var(--text-3);
  }
  .offline .list {
    opacity: 0.6;
  }
  .offline-note {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 12.5px;
    color: var(--text-3);
  }
  .list {
    overflow: hidden;
  }
  .list li + li {
    border-top: 1px solid var(--border);
  }
  .merged .title,
  .merged .id {
    color: var(--text-3);
  }
  .row {
    display: grid;
    grid-template-columns: 74px minmax(0, 1fr) auto 118px auto;
    align-items: center;
    gap: 10px;
    padding: 0 14px;
    height: 38px;
  }
  .row:hover {
    background: var(--surface-hover);
  }
  .row:hover .title {
    text-decoration: underline;
    text-underline-offset: 2px;
  }
  .id {
    font-size: 12px;
    color: var(--text-2);
    white-space: nowrap;
  }
  .title {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-weight: 500;
  }
  .tags {
    display: flex;
    gap: 6px;
  }
  .tag {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    height: 20px;
    padding: 0 7px;
    border-radius: 10px;
    font-size: 11.5px;
    font-weight: 600;
    white-space: nowrap;
    background: var(--surface-2);
    color: var(--text-2);
  }
  .tag.approvals {
    background: var(--info-bg);
    color: var(--info);
  }
  .tag.questions {
    background: var(--warn-bg);
    color: var(--warn);
  }
  .tag.deliver {
    background: var(--ok-bg);
    color: var(--ok);
  }
  .tag.blocked {
    background: var(--err-bg);
    color: var(--err);
  }
  .right {
    display: flex;
    align-items: center;
    gap: 10px;
    justify-content: flex-end;
    min-width: 76px;
  }
  .time {
    font-size: 12px;
    color: var(--text-3);
    white-space: nowrap;
  }
  .empty {
    color: var(--text-2);
    padding: 14px 16px;
  }
  .done summary {
    cursor: pointer;
    font-size: 12.5px;
    font-weight: 600;
    color: var(--text-2);
    padding: 4px 2px;
  }
  .done[open] summary {
    margin-bottom: 8px;
  }

  @media (max-width: 640px) {
    .board {
      padding: 14px 12px 32px;
      gap: 18px;
    }
    h1 {
      display: none;
    }
    .filters {
      flex-wrap: nowrap;
      overflow-x: auto;
      margin: 0 -12px;
      padding: 0 12px 2px;
      scrollbar-width: none;
    }
    .row {
      grid-template-columns: auto auto minmax(0, 1fr) auto;
      grid-template-areas: 'title title title title' 'id state . right' 'tags tags tags tags';
      height: auto;
      padding: 9px 12px;
      row-gap: 4px;
      column-gap: 8px;
    }
    .id {
      grid-area: id;
    }
    .title {
      grid-area: title;
      white-space: normal;
    }
    .tags {
      grid-area: tags;
      flex-wrap: wrap;
    }
    .tags:empty {
      display: none;
    }
    .right {
      grid-area: right;
      min-width: 0;
    }
    .state {
      grid-area: state;
    }
    .banner {
      flex-wrap: wrap;
    }
  }
</style>
