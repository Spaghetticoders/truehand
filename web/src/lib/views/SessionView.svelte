<script lang="ts">
  import Dot from '../components/Dot.svelte'
  import Icon from '../components/Icon.svelte'
  import Terminal from '../components/Terminal.svelte'
  import OfflineBanner from '../components/OfflineBanner.svelte'
  import { api } from '../api'
  import { t, timeAgo } from '../i18n/index.svelte'
  import { app, dotOf, isLive, project, statusKey, taskOf } from '../stores/app.svelte'
  import { href } from '../stores/router.svelte'
  import { errorText, toast } from '../stores/actions.svelte'
  import { sessionLabel } from '../actors'

  let { id }: { id: string } = $props()

  const s = $derived(app.data?.sessions.find((x) => x.id === id))
  const task = $derived(s?.taskId ? taskOf(s.projectId, s.taskId) : undefined)
  const live = $derived(!!s && isLive(s))

  const run = (f: () => Promise<unknown>) => f().catch((e) => toast('error', errorText(e)))
</script>

<div class="one">
  <nav class="back"><a href={href({ name: 'agents' })}><Icon name="chevron-left" size={18} />{t('mobile.agents')}</a></nav>
  <OfflineBanner />
  {#if s}
    <header>
      <p class="state"><Dot status={dotOf(s)} size={9} />{t(statusKey(s))}</p>
      <h1>{task?.title ?? t('sessions.free')}</h1>
      <p class="meta">{sessionLabel(s.role)} · {project(s.projectId)?.name ?? ''} · {s.endedAt ? t('sessions.ended', { time: timeAgo(s.endedAt) }) : t('sessions.started', { time: timeAgo(s.startedAt) })}</p>
    </header>
    <div class="term">
      {#key `${s.id}/${live}`}
        <Terminal sessionId={s.id} {live} label={t('task.terminal', { who: sessionLabel(s.role) })} />
      {/key}
    </div>
    <div class="tools">
      {#if live}
        <button class="btn" onclick={() => run(() => api.signalSession(s.id, 'int'))}><Icon name="stop" size={14} />{t('session.interrupt')}</button>
        <button class="btn ghost" onclick={() => run(() => api.signalSession(s.id, 'term'))}>{t('session.terminate')}</button>
      {:else if s.resumable}
        <button class="btn primary" onclick={() => run(() => api.resumeSession(s.id))}><Icon name="play" size={14} />{t('session.resume')}</button>
      {/if}
      {#if task}
        <a class="btn ghost" href={href({ name: 'task', project: s.projectId, id: task.id, tab: 'description', tabSet: true })}>{t('sessions.openTask')}</a>
      {/if}
    </div>
  {:else if !app.error && !app.offline}
    <p class="gone">{t('mobile.sessionGone')}</p>
  {/if}
</div>

<style>
  .one {
    display: grid;
    gap: 10px;
    padding: 6px 12px 16px;
  }
  .back a {
    display: inline-flex;
    align-items: center;
    gap: 2px;
    min-height: 44px;
    padding-right: 12px;
    color: var(--text-2);
    font-weight: 600;
    text-decoration: none;
  }
  header {
    display: grid;
    gap: 2px;
  }
  .state {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0;
    font-size: 13px;
    font-weight: 600;
  }
  h1 {
    margin: 0;
    font-size: 17px;
  }
  .meta {
    margin: 0;
    font-size: 12.5px;
    color: var(--text-3);
  }
  .term {
    min-height: 55dvh;
    display: grid;
  }
  .tools {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }
  .tools .btn {
    min-height: 44px;
  }
  .gone {
    color: var(--text-2);
  }
</style>
