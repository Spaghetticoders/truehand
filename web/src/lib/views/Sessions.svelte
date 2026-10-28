<script lang="ts">
  import Dot from '../components/Dot.svelte'
  import Icon from '../components/Icon.svelte'
  import Terminal from '../components/Terminal.svelte'
  import { api, type Session } from '../api'
  import { t, timeAgo } from '../i18n/index.svelte'
  import { app, clientName, dotOf, isLive, isRemote, machineOf, project, statusKey, taskOf } from '../stores/app.svelte'
  import { errorText, toast } from '../stores/actions.svelte'
  import { href } from '../stores/router.svelte'
  import { sessionLabel } from '../actors'

  const ORDER = { waiting: 0, error: 1, working: 2, idle: 3 }
  const sessions = $derived(
    [...(app.data?.sessions ?? [])].sort((a, b) => ORDER[dotOf(a)] - ORDER[dotOf(b)] || b.startedAt.localeCompare(a.startedAt)),
  )
  const working = $derived(sessions.filter((s) => dotOf(s) === 'working').length)
  const waiting = $derived(sessions.filter((s) => dotOf(s) === 'waiting').length)
  let open = $state<string | null>(null)

  const link = (s: Session) => (s.taskId ? href({ name: 'task', project: s.projectId, id: s.taskId, tab: 'agent', session: s.id }) : null)
  const resume = (s: Session) => api.resumeSession(s.id).catch((e) => toast('error', errorText(e)))
</script>

<div class="sessions">
  <header class="head">
    <h1>{t('sessions.title')}</h1>
    <p class="muted">
      {t('sessions.subtitle')}
      {#if sessions.length}
        <span class="sum">
          <span><Dot status="working" />{t('sessions.workingCount', { n: working })}</span>
          <span><Dot status="waiting" />{t('sessions.waitingCount', { n: waiting })}</span>
        </span>
      {/if}
    </p>
  </header>

  {#if sessions.length}
    <ul class="grid">
      {#each sessions as s (s.id)}
        {@const task = s.taskId ? taskOf(s.projectId, s.taskId) : undefined}
        {@const p = project(s.projectId)}
        {@const dot = dotOf(s)}
        <li class="card s {dot}" class:expanded={open === s.id}>
          <div class="s-head">
            <Dot status={dot} size={9} />
            <span class="status">{t(statusKey(s))}</span>
            <span class="who">{sessionLabel(s.role)}</span>
          </div>
          <div class="task">
            {#if s.taskId}
              <span class="id mono">{s.taskId}</span>
              <span class="title">{task?.title ?? ''}</span>
            {:else}
              <span class="title">{t('sessions.free')}</span>
            {/if}
          </div>
          <div class="where muted">{clientName(p?.clientId ?? '')} · {p?.name}{#if isRemote(s.projectId)} · {machineOf(s.projectId)?.label}{/if} · {s.endedAt ? t('sessions.ended', { time: timeAgo(s.endedAt) }) : t('sessions.started', { time: timeAgo(s.startedAt) })}</div>
          <div class="preview">
            {#key `${s.id}/${isLive(s)}`}
              <Terminal sessionId={s.id} live={open === s.id && isLive(s)} write={open === s.id} label={t('task.terminal', { who: sessionLabel(s.role) })} />
            {/key}
          </div>
          <div class="foot">
            {#if isLive(s)}
              <button class="btn sm" aria-expanded={open === s.id} onclick={() => (open = open === s.id ? null : s.id)}>
                <Icon name={open === s.id ? 'chevron-down' : 'keyboard'} size={14} />{open === s.id ? t('sessions.collapse') : t('sessions.answer')}
              </button>
            {:else if s.resumable}
              <button class="btn sm" onclick={() => resume(s)}><Icon name="play" size={14} />{t('session.resume')}</button>
            {/if}
            {#if link(s)}
              <a class="open" href={link(s)}>{t('sessions.openTask')}<Icon name="chevron-right" size={14} /></a>
            {/if}
          </div>
        </li>
      {/each}
    </ul>
  {:else}
    <p class="muted">{t('sessions.empty')}</p>
  {/if}
</div>

<style>
  .sessions {
    display: grid;
    gap: 18px;
    max-width: 1280px;
    margin: 0 auto;
    padding: 22px 28px 40px;
  }
  .head {
    display: grid;
    gap: 4px;
  }
  h1 {
    font-size: 20px;
  }
  .head p {
    display: flex;
    flex-wrap: wrap;
    gap: 6px 16px;
    align-items: center;
  }
  .sum {
    display: inline-flex;
    gap: 14px;
  }
  .sum span {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 13px;
  }
  .grid {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(360px, 1fr));
    gap: 14px;
  }
  .s {
    display: grid;
    gap: 8px;
    padding: 14px;
    min-width: 0;
  }
  .s.waiting {
    border-color: var(--dot-waiting);
  }
  .s.error {
    border-color: var(--err);
  }
  .s.expanded {
    grid-column: 1 / -1;
  }
  .s-head {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 12.5px;
  }
  .status {
    font-weight: 600;
  }
  .waiting .status {
    color: var(--warn);
  }
  .working .status {
    color: var(--ok);
  }
  .error .status {
    color: var(--err);
  }
  .idle .status {
    color: var(--text-3);
  }
  .who {
    margin-left: auto;
    color: var(--text-2);
  }
  .task {
    display: flex;
    gap: 8px;
    align-items: baseline;
    min-width: 0;
  }
  .id {
    font-size: 12px;
    color: var(--text-2);
  }
  .title {
    font-weight: 600;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .where {
    font-size: 12px;
  }
  .preview {
    height: 150px;
  }
  .expanded .preview {
    height: 420px;
  }
  .foot {
    display: flex;
    align-items: center;
    gap: 8px;
    min-height: 28px;
  }
  .open {
    margin-left: auto;
    display: inline-flex;
    align-items: center;
    gap: 2px;
    font-size: 12.5px;
    font-weight: 600;
    color: var(--text-2);
  }
  .open:hover {
    color: var(--text);
  }
  @media (max-width: 640px) {
    .sessions {
      padding: 14px 12px 32px;
    }
    h1 {
      display: none;
    }
    .grid {
      grid-template-columns: minmax(0, 1fr);
    }
    .expanded .preview {
      height: 360px;
    }
  }
</style>
