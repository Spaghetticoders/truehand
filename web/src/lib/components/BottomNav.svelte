<script lang="ts">
  import Icon, { type IconName } from './Icon.svelte'
  import { t, type Key } from '../i18n/index.svelte'
  import { href, router } from '../stores/router.svelte'
  import { agentsWorking, inboxItems } from '../stores/inbox.svelte'

  const count = $derived(inboxItems().length)
  const working = $derived(agentsWorking())
  const route = $derived(router.route)
  const current = $derived(route.name === 'board' && !route.project ? 'inbox' : route.name === 'sessions' || route.name === 'agents' || route.name === 'session' ? 'agents' : route.name === 'projects' || route.name === 'settings' || (route.name === 'board' && route.project) ? 'projects' : '')

  const TABS: { id: string; icon: IconName; label: Key; to: string }[] = [
    { id: 'inbox', icon: 'check', label: 'mobile.inbox', to: href({ name: 'board' }) },
    { id: 'agents', icon: 'sessions', label: 'mobile.agents', to: href({ name: 'agents' }) },
    { id: 'projects', icon: 'folder', label: 'mobile.projects', to: href({ name: 'projects' }) },
  ]
</script>

<nav class="bottom" aria-label={t('mobile.nav')}>
  {#each TABS as tab (tab.id)}
    <a href={tab.to} aria-current={current === tab.id ? 'page' : undefined}>
      <span class="ic">
        <Icon name={tab.icon} size={22} />
        {#if tab.id === 'inbox' && count}<span class="badge">{count}</span>{/if}
        {#if tab.id === 'agents' && working}<span class="live" aria-hidden="true"></span>{/if}
      </span>
      <span class="lb">{t(tab.label)}{#if tab.id === 'inbox' && count}<span class="sr-only"> ({count})</span>{/if}</span>
    </a>
  {/each}
</nav>

<style>
  .bottom {
    position: fixed;
    inset: auto 0 0 0;
    z-index: 30;
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    padding-bottom: env(safe-area-inset-bottom);
    border-top: 1px solid var(--border);
    background: var(--surface);
  }
  a {
    min-height: 56px;
    display: grid;
    place-items: center;
    align-content: center;
    gap: 2px;
    color: var(--text-3);
    font-size: 11.5px;
    font-weight: 600;
    text-decoration: none;
  }
  a[aria-current='page'] {
    color: var(--accent);
  }
  .ic {
    position: relative;
    display: grid;
  }
  .badge {
    position: absolute;
    top: -6px;
    right: -12px;
    min-width: 18px;
    height: 18px;
    padding: 0 5px;
    border-radius: 9px;
    background: var(--warn);
    color: var(--warn-bg);
    font-size: 11px;
    font-weight: 700;
    display: grid;
    place-items: center;
  }
  .live {
    position: absolute;
    top: -2px;
    right: -4px;
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--dot-working);
    box-shadow: 0 0 0 2px var(--surface);
  }
</style>
