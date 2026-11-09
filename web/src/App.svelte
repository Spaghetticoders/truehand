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
  import Inbox from './lib/views/Inbox.svelte'
  import Projects from './lib/views/Projects.svelte'
  import Agents from './lib/views/Agents.svelte'
  import SessionView from './lib/views/SessionView.svelte'
  import BottomNav from './lib/components/BottomNav.svelte'
  import { layout } from './lib/stores/layout.svelte'
  import { trackScopes } from './lib/stores/scope.svelte'
  import Enroll from './lib/views/Enroll.svelte'
  import SignSheet from './lib/components/SignSheet.svelte'
  import Toasts from './lib/components/Toasts.svelte'
  import NewTaskModal from './lib/views/NewTaskModal.svelte'
  import SendBackModal from './lib/views/SendBackModal.svelte'
  import AddProjectModal from './lib/views/AddProjectModal.svelte'
  import { t } from './lib/i18n/index.svelte'
  import { app, applyTheme, startSync, taskOf } from './lib/stores/app.svelte'
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
  const phone = $derived(layout.phone)
  // On the phone the home is the list of decisions; a task waiting for its plan opens on the plan.
  const inboxHome = $derived(phone && route.name === 'board' && !route.project)
  const taskTab = $derived(route.name === 'task' ? (phone && !route.tabSet && taskOf(route.project, route.id)?.planReady ? 'plan' : route.tab) : 'description')
  const waiting = $derived(app.data?.tasks.some((x) => !x.readOnly && !!x.waitReason) ?? false)
  const title = $derived(
    inboxHome
      ? t('mobile.inbox')
      : route.name === 'agents' || route.name === 'session'
        ? t('mobile.agents')
        : route.name === 'projects'
          ? t('mobile.projects')
          : route.name === 'task'
      ? route.id
      : route.name === 'sessions'
        ? t('nav.sessions')
        : route.name === 'settings'
          ? t('nav.settings')
          : route.name === 'activate'
            ? t('action.activate')
            : route.name === 'pair'
              ? t('settings.pairPhone')
              : route.name === 'enroll'
                ? t('enroll.title')
                : t('nav.board'),
  )

  // The phone's inbox needs each task's out-of-scope files: the snapshot does not carry them.
  $effect(() => {
    if (phone && app.data) trackScopes(app.data.tasks)
  })

  // A new page starts at its top: the scroll of the list it came from would hide its header.
  let mainEl = $state<HTMLElement>()
  const pageKey = $derived(route.name === 'task' ? `task/${route.project}/${route.id}` : route.name === 'board' ? `board/${route.project ?? ''}` : route.name)
  // Going back to the page before restores where it was; any other move starts at the top.
  const trail: { key: string; top: number }[] = []
  let lastKey = ''
  $effect(() => {
    const key = pageKey
    if (!mainEl || key === lastKey) return
    const el = mainEl
    if (trail.length) trail[trail.length - 1].top = savedTop
    const back = trail.length > 1 && trail[trail.length - 2].key === key
    if (back) trail.pop()
    else trail.push({ key, top: 0 })
    if (trail.length > 30) trail.shift()
    lastKey = key
    const top = back ? trail[trail.length - 1].top : 0
    // The page renders after the route changes: restore once its content is there.
    requestAnimationFrame(() => (el.scrollTop = top))
  })
  // The scroll of the page being left, read before the new one replaces it.
  let savedTop = 0
  $effect(() => {
    const el = mainEl
    if (!el) return
    const onScroll = () => (savedTop = el.scrollTop)
    el.addEventListener('scroll', onScroll, { passive: true })
    return () => el.removeEventListener('scroll', onScroll)
  })

  $effect(() => {
    document.title = `${title} · ${t('app.name')}`
  })
</script>

<svelte:window onkeydown={(e) => e.key === 'Escape' && (ui.drawer = false)} />

<a class="skip" href="#main">{t('app.skip')}</a>
{#if app.needsAuth}
  <main id="main" class="bare">{#if route.name === 'enroll'}<Enroll code={route.code} />{:else}<Pair />{/if}</main>
{:else}
<div class="shell">
  <aside class="side" class:open={ui.drawer}>
    <Sidebar />
  </aside>
  {#if ui.drawer}
    <button class="scrim" aria-label={t('app.closeMenu')} onclick={() => (ui.drawer = false)}></button>
  {/if}
  <div class="content" class:with-nav={phone}>
    <header class="bar">
      <button class="btn ghost icon menu" aria-label={t('app.menu')} aria-expanded={ui.drawer} onclick={() => (ui.drawer = true)}>
        <Icon name="menu" size={20} />
        {#if waiting}<span class="ping" aria-hidden="true"></span>{/if}
      </button>
      <span class="bar-title">{title}</span>
      {#if !phone}
        <button class="btn primary icon" aria-label={t('nav.newTask')} onclick={() => openNewTask(route.name === 'board' || route.name === 'task' || route.name === 'activate' ? (route.project ?? '') : '')}>
          <Icon name="plus" size={18} />
        </button>
      {/if}
    </header>
    {#if app.data?.me.readOnly}<p class="ro" role="status">{t('app.readOnly')}</p>{/if}
    <main id="main" tabindex="-1" bind:this={mainEl}>
      {#if app.error && !app.ready}
        <p class="loading">{app.error}</p>
      {:else if !app.ready}
        <p class="loading">{t('app.loading')}</p>
      {:else if route.name === 'task'}
        {#key `${route.project}/${route.id}`}
          <Task projectId={route.project} taskId={route.id} tab={taskTab} sessionId={route.session} />
        {/key}
      {:else if inboxHome}
        <Inbox />
      {:else if route.name === 'agents' && phone}
        <Agents />
      {:else if route.name === 'session'}
        <SessionView id={route.id} />
      {:else if route.name === 'sessions' || route.name === 'agents'}
        <Sessions />
      {:else if route.name === 'projects'}
        <Projects />
      {:else if route.name === 'settings'}
        <Settings />
      {:else if route.name === 'activate'}
        {#key route.project}<Activate projectId={route.project} />{/key}
      {:else if route.name === 'pair'}
        <Pair code={route.code} />
      {:else if route.name === 'enroll'}
        <Enroll code={route.code} />
      {:else}
        <Board />
      {/if}
    </main>
  </div>
</div>

{#if phone}<BottomNav />{/if}

<NewTaskModal />
<AddProjectModal />
<SendBackModal />
{/if}
<SignSheet />
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
  @media (max-width: 640px) {
    .bar .btn.icon {
      width: 44px;
      height: 44px;
    }
  }
  .with-nav main {
    padding-bottom: calc(64px + env(safe-area-inset-bottom));
  }
  @media (prefers-reduced-motion: reduce) {
    .side {
      transition: none;
    }
  }
</style>
