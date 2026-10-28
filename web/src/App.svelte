<script lang="ts">
  import { onMount } from 'svelte'
  import Sidebar from './lib/components/Sidebar.svelte'
  import Icon from './lib/components/Icon.svelte'
  import Board from './lib/views/Board.svelte'
  import Task from './lib/views/Task.svelte'
  import Sessions from './lib/views/Sessions.svelte'
  import Settings from './lib/views/Settings.svelte'
  import Activate from './lib/views/Activate.svelte'
  import Pair from './lib/views/Pair.svelte'
  import Toasts from './lib/components/Toasts.svelte'
  import NewTaskModal from './lib/views/NewTaskModal.svelte'
  import SendBackModal from './lib/views/SendBackModal.svelte'
  import AddProjectModal from './lib/views/AddProjectModal.svelte'
  import { t } from './lib/i18n/index.svelte'
  import { app, applyTheme, startSync } from './lib/stores/app.svelte'
  import { router, startRouter } from './lib/stores/router.svelte'
  import { openNewTask, ui } from './lib/stores/ui.svelte'

  onMount(() => {
    applyTheme()
    const stopSync = startSync()
    const stopRouter = startRouter()
    return () => {
      stopSync()
      stopRouter()
    }
  })

  const route = $derived(router.route)
  const waiting = $derived(app.data?.tasks.some((x) => !x.readOnly && !!x.waitReason) ?? false)
  const title = $derived(
    route.name === 'task'
      ? route.id
      : route.name === 'sessions'
        ? t('nav.sessions')
        : route.name === 'settings'
          ? t('nav.settings')
          : route.name === 'activate'
            ? t('action.activate')
            : route.name === 'pair'
              ? t('settings.pairPhone')
              : t('nav.board'),
  )

  $effect(() => {
    document.title = `${title} · ${t('app.name')}`
  })
</script>

<svelte:window onkeydown={(e) => e.key === 'Escape' && (ui.drawer = false)} />

<a class="skip" href="#main">{t('app.skip')}</a>
{#if app.needsAuth}
  <main id="main" class="bare"><Pair /></main>
{:else}
<div class="shell">
  <aside class="side" class:open={ui.drawer}>
    <Sidebar />
  </aside>
  {#if ui.drawer}
    <button class="scrim" aria-label={t('app.closeMenu')} onclick={() => (ui.drawer = false)}></button>
  {/if}
  <div class="content">
    <header class="bar">
      <button class="btn ghost icon menu" aria-label={t('app.menu')} aria-expanded={ui.drawer} onclick={() => (ui.drawer = true)}>
        <Icon name="menu" size={20} />
        {#if waiting}<span class="ping" aria-hidden="true"></span>{/if}
      </button>
      <span class="bar-title">{title}</span>
      <button class="btn primary icon" aria-label={t('nav.newTask')} onclick={() => openNewTask(route.name === 'board' || route.name === 'task' || route.name === 'activate' ? (route.project ?? '') : '')}>
        <Icon name="plus" size={18} />
      </button>
    </header>
    {#if app.data?.me.readOnly}<p class="ro" role="status">{t('app.readOnly')}</p>{/if}
    <main id="main" tabindex="-1">
      {#if app.error && !app.ready}
        <p class="loading">{app.error}</p>
      {:else if !app.ready}
        <p class="loading">{t('app.loading')}</p>
      {:else if route.name === 'task'}
        {#key `${route.project}/${route.id}`}
          <Task projectId={route.project} taskId={route.id} tab={route.tab} sessionId={route.session} />
        {/key}
      {:else if route.name === 'sessions'}
        <Sessions />
      {:else if route.name === 'settings'}
        <Settings />
      {:else if route.name === 'activate'}
        {#key route.project}<Activate projectId={route.project} />{/key}
      {:else if route.name === 'pair'}
        <Pair code={route.code} />
      {:else}
        <Board />
      {/if}
    </main>
  </div>
</div>

<NewTaskModal />
<AddProjectModal />
<SendBackModal />
{/if}
<Toasts />

<style>
  .skip {
    position: absolute;
    left: 8px;
    top: -40px;
    z-index: 100;
    padding: 6px 10px;
    border-radius: var(--radius-sm);
    background: var(--accent);
    color: var(--accent-fg);
  }
  .skip:focus {
    top: 8px;
  }
  .shell {
    display: grid;
    grid-template-columns: var(--sidebar-w) minmax(0, 1fr);
    height: 100dvh;
  }
  .side {
    min-height: 0;
  }
  .content {
    min-width: 0;
    min-height: 0;
    display: flex;
    flex-direction: column;
  }
  main {
    flex: 1;
    min-height: 0;
    overflow: auto;
    outline: none;
  }
  .bar {
    display: none;
  }
  .ro {
    margin: 0;
    padding: 6px 16px;
    background: var(--warn-bg);
    color: var(--warn);
    font-size: 12.5px;
    font-weight: 600;
    text-align: center;
  }
  .bare {
    min-height: 100dvh;
    padding-top: 6vh;
  }
  .scrim {
    display: none;
  }
  .loading {
    padding: 40px;
    color: var(--text-2);
  }

  @media (max-width: 900px) {
    .shell {
      grid-template-columns: minmax(0, 1fr);
    }
    .side {
      position: fixed;
      inset: 0 auto 0 0;
      width: min(300px, 86vw);
      z-index: 50;
      transform: translateX(-100%);
      transition: transform 0.18s ease;
      visibility: hidden;
    }
    .side.open {
      transform: none;
      visibility: visible;
      box-shadow: var(--shadow);
    }
    .scrim {
      display: block;
      position: fixed;
      inset: 0;
      z-index: 40;
      border: 0;
      background: var(--overlay);
    }
    .bar {
      display: flex;
      align-items: center;
      gap: 8px;
      height: 52px;
      padding: 0 10px;
      padding-top: env(safe-area-inset-top);
      border-bottom: 1px solid var(--border);
      background: var(--surface);
      flex: none;
    }
    .menu {
      position: relative;
    }
    .ping {
      position: absolute;
      top: 6px;
      right: 6px;
      width: 8px;
      height: 8px;
      border-radius: 50%;
      background: var(--dot-waiting);
      box-shadow: 0 0 0 2px var(--surface);
    }
    .bar-title {
      flex: 1;
      min-width: 0;
      font-weight: 600;
      font-size: 15px;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .side {
      transition: none;
    }
  }
</style>
