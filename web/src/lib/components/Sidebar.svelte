<script lang="ts">
  import Icon from './Icon.svelte'
  import Dot from './Dot.svelte'
  import { t } from '../i18n/index.svelte'
  import { app, clientName, crashed, dotOf, isOffline, machineOf, reasonsOf } from '../stores/app.svelte'
  import { href, router } from '../stores/router.svelte'
  import { openNewTask, ui } from '../stores/ui.svelte'
  import { sessionLabel } from '../actors'

  const route = $derived(router.route)
  const data = $derived(app.data)
  const waitingCount = $derived((data?.tasks.filter((x) => !x.readOnly && reasonsOf(x).length).length ?? 0) + (data?.sessions.filter((s) => !s.taskId && s.status === 'waiting').length ?? 0))
  const currentProject = $derived(route.name === 'board' || route.name === 'task' || route.name === 'activate' ? route.project : undefined)
  const currentSession = $derived(route.name === 'task' && route.tab === 'agent' ? route.session : undefined)
  const shown = (pid: string) => data?.sessions.filter((s) => s.projectId === pid && (s.status !== 'exited' || crashed(s))) ?? []
  const waitingIn = (pid: string) => data?.tasks.filter((x) => x.projectId === pid && !x.readOnly && reasonsOf(x).length).length ?? 0

  const close = () => (ui.drawer = false)
</script>

<nav class="sidebar" aria-label={t('app.name')}>
  <div class="brand">
    <span class="mark" aria-hidden="true">{t('app.name').charAt(0)}</span>
    <span class="name">{t('app.name')}</span>
  </div>

  <button class="btn primary new" onclick={() => (close(), openNewTask())}>
    <Icon name="plus" />{t('nav.newTask')}
  </button>

  <ul class="nav">
    <li>
      <a href={href({ name: 'board' })} onclick={close} aria-current={route.name === 'board' && !route.project ? 'page' : undefined}>
        <Icon name="board" />{t('nav.board')}
        {#if waitingCount}<span class="badge" title={t('board.waiting')}>{waitingCount}</span>{/if}
      </a>
    </li>
    <li>
      <a href={href({ name: 'sessions' })} onclick={close} aria-current={route.name === 'sessions' ? 'page' : undefined}>
        <Icon name="sessions" />{t('nav.sessions')}
        {#if data?.sessions.some((s) => s.status === 'working')}<span class="live"><Dot status="working" size={7} /></span>{/if}
      </a>
    </li>
    <li>
      <a href={href({ name: 'settings' })} onclick={close} aria-current={route.name === 'settings' ? 'page' : undefined}>
        <Icon name="settings" />{t('nav.settings')}
      </a>
    </li>
  </ul>

  <div class="tree">
    <h2 class="section">{t('nav.projects')}</h2>
    {#each data?.clients ?? [] as c (c.id)}
      {@const projects = data?.projects.filter((p) => p.clientId === c.id) ?? []}
      {#if projects.length}
        <div class="client">{clientName(c.id)}</div>
        <ul>
          {#each projects as p (p.id)}
            {@const sessions = shown(p.id)}
            {@const m = machineOf(p.id)}
            {@const n = waitingIn(p.id)}
            <li class:offline={isOffline(p.id)}>
              <a class="project" href={href({ name: 'board', project: p.id })} onclick={close} aria-current={currentProject === p.id ? 'page' : undefined}>
                <Icon name="folder" size={14} />
                <span class="ellipsis">{p.name}</span>
                {#if m && m.id !== 'local'}<span class="machine" title={t(`machine.status.${m.status}`)}>{m.label}</span>{/if}
                {#if p.configError}<span class="off err">{t('nav.configError')}</span>
                {:else if !p.active}<span class="off">{t('nav.notActive')}</span>
                {:else if n}<span class="count">{n}</span>{/if}
              </a>
              {#if sessions.length}
                <ul class="sessions">
                  {#each sessions as s (s.id)}
                    <li>
                      <a
                        href={s.taskId ? href({ name: 'task', project: p.id, id: s.taskId, tab: 'agent', session: s.id }) : href({ name: 'sessions' })}
                        onclick={close}
                        aria-current={currentSession === s.id ? 'page' : undefined}
                      >
                        <Dot status={dotOf(s)} />
                        {#if s.taskId}<span class="sid mono">{s.taskId}</span>{/if}
                        <span class="ellipsis role">{sessionLabel(s.role)}</span>
                      </a>
                    </li>
                  {/each}
                </ul>
              {/if}
            </li>
          {/each}
        </ul>
      {/if}
    {/each}
  </div>

  <button class="btn ghost add" onclick={() => (close(), (ui.addProject = true))}>
    <Icon name="plus" size={14} />{t('nav.addProject')}
  </button>
</nav>

<style>
  .sidebar {
    display: flex;
    flex-direction: column;
    gap: 6px;
    height: 100%;
    padding: 14px 10px 10px;
    background: var(--sidebar);
    border-right: 1px solid var(--border);
    overflow: hidden;
  }
  .brand {
    display: flex;
    align-items: center;
    gap: 9px;
    padding: 2px 8px 8px;
    font-weight: 700;
    font-size: 15px;
  }
  .mark {
    display: grid;
    place-items: center;
    width: 24px;
    height: 24px;
    border-radius: 6px;
    background: var(--brand-navy);
    color: var(--brand-cyan);
    font-size: 14px;
    box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--brand-cyan) 30%, transparent);
  }
  .new {
    margin: 0 4px 6px;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .nav a,
  .project,
  .sessions a {
    display: flex;
    align-items: center;
    gap: 9px;
    height: 30px;
    padding: 0 8px;
    border-radius: var(--radius-sm);
    color: var(--text-2);
    font-weight: 500;
  }
  .nav a:hover,
  .project:hover,
  .sessions a:hover {
    background: var(--surface-hover);
    color: var(--text);
  }
  [aria-current='page'] {
    background: var(--surface-hover);
    color: var(--text) !important;
  }
  .badge {
    margin-left: auto;
    min-width: 18px;
    height: 18px;
    padding: 0 5px;
    border-radius: 9px;
    background: var(--warn-bg);
    color: var(--warn);
    font-size: 11px;
    font-weight: 700;
    display: grid;
    place-items: center;
  }
  .tree {
    flex: 1;
    min-height: 0;
    overflow: auto;
    margin-top: 10px;
  }
  .section {
    padding: 4px 8px;
    font-size: 11.5px;
    font-weight: 600;
    color: var(--text-3);
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .client {
    padding: 10px 8px 2px;
    font-size: 12px;
    font-weight: 600;
    color: var(--text-3);
  }
  .project {
    font-size: 13.5px;
  }
  .live {
    margin-left: auto;
    display: inline-flex;
  }
  .count {
    margin-left: auto;
    min-width: 18px;
    height: 18px;
    padding: 0 5px;
    border-radius: 9px;
    background: var(--warn-bg);
    color: var(--warn);
    font-size: 11px;
    font-weight: 700;
    display: grid;
    place-items: center;
  }
  .machine {
    flex: none;
    margin-left: auto;
    padding: 0 6px;
    border-radius: 8px;
    border: 1px solid var(--border-strong);
    font-size: 10.5px;
    font-weight: 600;
    color: var(--text-2);
    white-space: nowrap;
  }
  .machine + .off,
  .machine + .count {
    margin-left: 4px;
  }
  .offline .project,
  .offline .sessions a {
    color: var(--text-3);
  }
  .offline .machine {
    border-style: dashed;
  }
  .off.err {
    color: var(--err);
  }
  .off {
    margin-left: auto;
    font-size: 11px;
    color: var(--text-3);
    white-space: nowrap;
  }
  .sessions a {
    height: 26px;
    padding-left: 30px;
    font-size: 12.5px;
    gap: 7px;
  }
  .sid {
    flex: none;
    font-size: 11.5px;
    color: var(--text);
    white-space: nowrap;
  }
  .role {
    color: var(--text-3);
  }
  .ellipsis {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .add {
    justify-content: flex-start;
    color: var(--text-2);
  }
</style>
