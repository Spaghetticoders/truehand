<script lang="ts">
  import Icon from '../components/Icon.svelte'
  import InboxCard from '../components/InboxCard.svelte'
  import OfflineBanner from '../components/OfflineBanner.svelte'
  import DecisionSheet from './DecisionSheet.svelte'
  import { t } from '../i18n/index.svelte'
  import { app, sessionsOf } from '../stores/app.svelte'
  import { known } from '../passkeys'
  import { go, href } from '../stores/router.svelte'
  import { ui } from '../stores/ui.svelte'
  import { agentsWorking, inboxItems, type InboxItem } from '../stores/inbox.svelte'
  import { shell } from '../api/http'

  const items = $derived(inboxItems())
  const working = $derived(agentsWorking())
  const noProjects = $derived(!!app.data && !app.data.projects.length)
  // A server without the Mac needs a passkey here to approve anything.
  const needsPasskey = $derived(!!app.data?.me.headless && !shell() && !app.data?.me.owner && !known().length)

  let sheet = $state<{ pid: string; id: string } | null>(null)
  let sheetOpen = $state(false)

  function openItem(i: InboxItem) {
    const task = i.task
    if (i.kind === 'plan' && task) {
      sheet = { pid: task.projectId, id: task.id }
      sheetOpen = true
      return
    }
    if (task && (i.kind === 'question' || i.kind === 'permission')) {
      const s = sessionsOf(task.projectId, task.id).find((x) => x.status === 'waiting')
      return go({ name: 'task', project: task.projectId, id: task.id, tab: 'agent', session: s?.id })
    }
    if (task) return go({ name: 'task', project: task.projectId, id: task.id, tab: 'description' })
    if (i.session) return go({ name: 'agents' })
    if (i.queued) return go({ name: 'activate', project: i.projectId })
  }
</script>

<div class="inbox">
  <h1 class="sr-only">{t('mobile.inbox')}</h1>

  <OfflineBanner />
  {#if needsPasskey}
    <a class="state warn" href={href({ name: 'enroll' })}><Icon name="lock" /><span>{t('inbox.noPasskey')}</span><Icon name="chevron-right" /></a>
  {/if}

  {#if items.length}
    <ul class="cards">
      {#each items as i (i.key)}<InboxCard item={i} onopen={openItem} />{/each}
    </ul>
  {:else if app.error || app.offline}
    <!-- Offline, an empty list says nothing: the banner above is the state. -->
  {:else if noProjects}
    <div class="empty">
      <Icon name="folder" size={28} />
      <p>{t('inbox.noProjects')}</p>
      <button class="btn primary big" onclick={() => (ui.addProject = true)}><Icon name="plus" />{t('nav.addProject')}</button>
    </div>
  {:else if working}
    <div class="empty">
      <Icon name="sessions" size={28} />
      <p>{t('inbox.agentsWorking')}</p>
      <a class="btn big" href={href({ name: 'agents' })}>{t('inbox.seeAgents')}</a>
    </div>
  {:else}
    <div class="empty">
      <Icon name="check" size={28} />
      <p>{t('inbox.allQuiet')}</p>
      <a class="btn big" href={href({ name: 'projects' })}>{t('inbox.seeProjects')}</a>
    </div>
  {/if}
</div>

{#if sheet}<DecisionSheet bind:open={sheetOpen} projectId={sheet.pid} taskId={sheet.id} />{/if}

<style>
  .inbox {
    display: grid;
    gap: 12px;
    padding: 14px 12px 24px;
  }
  .cards {
    display: grid;
    gap: 10px;
    margin: 0;
    padding: 0;
  }
  .state {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0;
    padding: 12px;
    min-height: 44px;
    border-radius: var(--radius);
    font-size: 14px;
    text-decoration: none;
  }
  .state span {
    flex: 1;
  }
  .state.warn {
    background: var(--warn-bg);
    color: var(--warn);
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
    font-size: 15px;
  }
  .big {
    min-height: 44px;
    padding: 0 18px;
  }
</style>
