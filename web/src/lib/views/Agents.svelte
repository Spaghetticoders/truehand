<script lang="ts">
  import Dot from '../components/Dot.svelte'
  import Icon from '../components/Icon.svelte'
  import Reveal from '../components/Reveal.svelte'
  import OfflineBanner from '../components/OfflineBanner.svelte'
  import type { Session, TaskSummary } from '../api'
  import { t, timeAgo } from '../i18n/index.svelte'
  import { app, crashed, dotOf, project, statusKey, taskOf } from '../stores/app.svelte'
  import { href } from '../stores/router.svelte'
  import { sessionLabel } from '../actors'

  const ORDER = { waiting: 0, error: 1, working: 2, idle: 3 }
  // Closed sessions only matter when they broke.
  const sessions = $derived(
    (app.data?.sessions ?? []).filter((s) => s.status !== 'exited' || crashed(s)).sort((a, b) => ORDER[dotOf(a)] - ORDER[dotOf(b)] || b.startedAt.localeCompare(a.startedAt)),
  )
  // Work only the agent can do: failing checks, proofs gone stale.
  const agentWork = $derived((app.data?.tasks ?? []).filter((x) => !x.readOnly && !x.merged && (x.failing || x.stale)))
  const down = $derived(!!app.error || app.offline)

  const lastLine = (s: Session) => s.tail.filter((l) => l.trim()).at(-1) ?? ''
  const sessionHref = (s: Session) => (s.taskId ? href({ name: 'task', project: s.projectId, id: s.taskId, tab: 'agent', session: s.id, tabSet: true }) : href({ name: 'session', id: s.id }))
  const taskHref = (x: TaskSummary) => href({ name: 'task', project: x.projectId, id: x.id, tab: 'agent', tabSet: true })
</script>

<div class="agents">
  <h1 class="sr-only">{t('mobile.agents')}</h1>
  <OfflineBanner />

  {#if agentWork.length}
    <section aria-labelledby="agent-work">
      <h2 id="agent-work">{t('agents.theirs')}</h2>
      <p class="hint">{t('agents.theirsHint')}</p>
      <ul class="cards">
        {#each agentWork as x (`${x.projectId}/${x.id}`)}
          <li>
            <a class="card" href={taskHref(x)}>
              <span class="chip {x.failing ? 'err' : 'warn'}"><Icon name="alert" size={13} />{t(x.failing ? 'agents.failing' : 'agents.stale')}</span>
              <span class="title">{x.title}</span>
              <span class="meta">{x.id} · {project(x.projectId)?.name ?? ''} · {timeAgo(x.updatedAt)}</span>
            </a>
          </li>
        {/each}
      </ul>
    </section>
  {/if}

  {#if sessions.length}
    <section aria-labelledby="agent-sessions">
      <h2 id="agent-sessions">{t('agents.sessions')}</h2>
      <ul class="cards">
        {#each sessions as s (s.id)}
          {@const task = s.taskId ? taskOf(s.projectId, s.taskId) : undefined}
          {@const line = lastLine(s)}
          <li>
            <a class="card" href={sessionHref(s)}>
              <span class="state"><Dot status={dotOf(s)} size={9} />{t(statusKey(s))}</span>
              <span class="title">{task?.title ?? t('sessions.free')}</span>
              {#if line}<span class="line"><span class="clamp"><Reveal text={line} /></span></span>{/if}
              <span class="meta">{sessionLabel(s.role)} · {project(s.projectId)?.name ?? ''} · {s.endedAt ? t('sessions.ended', { time: timeAgo(s.endedAt) }) : t('sessions.started', { time: timeAgo(s.startedAt) })}</span>
            </a>
          </li>
        {/each}
      </ul>
    </section>
  {:else if !down && !agentWork.length}
    <div class="empty">
      <Icon name="sessions" size={28} />
      <p>{t('agents.none')}</p>
    </div>
  {/if}
</div>

<style>
  .agents {
    display: grid;
    gap: 14px;
    padding: 14px 12px 24px;
  }
  section {
    display: grid;
    gap: 8px;
  }
  h2 {
    margin: 0;
    font-size: 13px;
    font-weight: 600;
    color: var(--text-2);
  }
  .hint {
    margin: -4px 0 0;
    font-size: 12.5px;
    color: var(--text-3);
  }
  .cards {
    display: grid;
    gap: 8px;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .card {
    display: grid;
    gap: 5px;
    min-height: 44px;
    padding: 12px 14px;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
    color: inherit;
    text-decoration: none;
  }
  .state {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 13px;
    font-weight: 600;
  }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    justify-self: start;
    padding: 2px 8px;
    border-radius: 10px;
    font-size: 12px;
    font-weight: 600;
  }
  .chip.err {
    background: var(--err-bg);
    color: var(--err);
  }
  .chip.warn {
    background: var(--warn-bg);
    color: var(--warn);
  }
  .title {
    font-size: 15px;
    font-weight: 600;
  }
  .line {
    font-family: var(--mono);
    font-size: 12px;
    padding: 6px 8px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    color: var(--text-2);
  }
  .clamp {
    display: -webkit-box;
    -webkit-box-orient: vertical;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    overflow: hidden;
    overflow-wrap: anywhere;
  }
  .meta {
    font-size: 12.5px;
    color: var(--text-3);
  }
  .empty {
    display: grid;
    justify-items: center;
    gap: 12px;
    padding: 40px 16px;
    text-align: center;
    color: var(--text-2);
  }
  .empty p {
    margin: 0;
  }
</style>
