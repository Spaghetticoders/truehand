<script lang="ts">
  import Icon from '../components/Icon.svelte'
  import { t } from '../i18n/index.svelte'
  import { app, clientName } from '../stores/app.svelte'
  import { href } from '../stores/router.svelte'
  import { openNewTask, ui } from '../stores/ui.svelte'
  import { inboxItems } from '../stores/inbox.svelte'

  const waiting = $derived.by(() => {
    const n: Record<string, number> = {}
    for (const i of inboxItems()) n[i.projectId] = (n[i.projectId] ?? 0) + 1
    return n
  })
</script>

<div class="projects">
  <h1 class="sr-only">{t('mobile.projects')}</h1>
  <button class="btn primary big" onclick={() => (ui.addProject = true)}><Icon name="plus" />{t('nav.addProject')}</button>

  {#each app.data?.clients ?? [] as c (c.id)}
    {@const list = app.data?.projects.filter((p) => p.clientId === c.id) ?? []}
    {#if list.length}
      <h2>{clientName(c.id)}</h2>
      <ul>
        {#each list as p (p.id)}
          <li>
            <a class="open" href={p.active ? href({ name: 'board', project: p.id }) : href({ name: 'activate', project: p.id })}>
              <span class="name">{p.name}</span>
              <span class="meta">
                {#if p.configError}{t('nav.configError')}{:else if !p.active}{t('nav.notActive')}{:else if waiting[p.id]}{t('mobile.waitingN', { n: waiting[p.id] })}{:else}{t('mobile.nothingWaiting')}{/if}
              </span>
            </a>
            {#if p.active}
              <button class="btn sm-touch" aria-label={t('mobile.newTaskIn', { name: p.name })} onclick={() => openNewTask(p.id)}><Icon name="plus" />{t('nav.newTask')}</button>
            {/if}
          </li>
        {/each}
      </ul>
    {/if}
  {/each}

  <a class="settings" href={href({ name: 'settings' })}><Icon name="settings" /><span>{t('nav.settings')}</span><Icon name="chevron-right" /></a>
</div>

<style>
  .projects {
    display: grid;
    gap: 12px;
    padding: 14px 12px 24px;
  }
  h2 {
    margin: 6px 0 0;
    font-size: 12.5px;
    font-weight: 600;
    color: var(--text-3);
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
  }
  li {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 8px 6px 0;
  }
  li + li {
    border-top: 1px solid var(--border);
  }
  .open {
    flex: 1;
    min-width: 0;
    min-height: 48px;
    display: grid;
    align-content: center;
    padding-left: 14px;
    color: inherit;
    text-decoration: none;
  }
  .name {
    font-weight: 600;
    font-size: 15px;
  }
  .meta {
    font-size: 12.5px;
    color: var(--text-3);
  }
  .big,
  .sm-touch {
    min-height: 44px;
  }
  .big {
    justify-content: center;
  }
  .settings {
    display: flex;
    align-items: center;
    gap: 10px;
    min-height: 48px;
    padding: 0 14px;
    margin-top: 8px;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
    color: inherit;
    text-decoration: none;
  }
  .settings span {
    flex: 1;
  }
</style>
