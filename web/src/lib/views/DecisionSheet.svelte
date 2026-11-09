<script lang="ts">
  import Icon from '../components/Icon.svelte'
  import Modal from '../components/Modal.svelte'
  import Markdown from '../components/Markdown.svelte'
  import InvisibleNotice from '../components/InvisibleNotice.svelte'
  import { api, type TaskDetail } from '../api'
  import { t } from '../i18n/index.svelte'
  import { project, sessionsOf, taskOf } from '../stores/app.svelte'
  import { go, href } from '../stores/router.svelte'
  import { ui } from '../stores/ui.svelte'
  import { errorText, runAction } from '../stores/actions.svelte'
  import { planBrief } from '../plan'

  let { open = $bindable(false), projectId, taskId }: { open?: boolean; projectId: string; taskId: string } = $props()

  let detail = $state<TaskDetail | null>(null)
  let error = $state<string | null>(null)

  // The person approves the whole plan, not the agent's summary: the full text must have been opened.
  let read = $state(false)

  $effect(() => {
    if (!open) return
    detail = null
    error = null
    read = false
    api
      .task(projectId, taskId)
      .then((d) => (detail = d))
      .catch((e) => (error = errorText(e)))
  })

  const brief = $derived(planBrief(detail?.plan ?? ''))
  const sections = $derived((detail?.plan ?? '').match(/^##\s/gm)?.length ?? 0)
  const code = $derived(detail?.planSha256?.slice(0, 8) ?? '')
  const proj = $derived(project(projectId))
  // The agent may also be waiting on a question: approving does not answer it.
  const asking = $derived(!!detail?.hasOpenQuestion || sessionsOf(projectId, taskId).some((s) => s.status === 'waiting'))

  async function approve() {
    const task = taskOf(projectId, taskId)
    if (!task || !detail?.planSha256 || !read) return
    const shown = detail.planSha256
    open = false
    // Approves the plan read here: if it changed meanwhile, the server refuses it.
    await runAction(task, 'approve-plan', { planSha256: shown })
  }

  function sendBack() {
    open = false
    ui.sendBack = { pid: projectId, id: taskId }
  }

  function openTask() {
    open = false
    go({ name: 'task', project: projectId, id: taskId, tab: 'plan' })
  }
</script>

<Modal bind:open title={t('decision.planTitle')} onsubmit={() => {}}>
  <div class="sheet">
    {#if detail}
      <p class="ctx"><strong>{detail.title}</strong><span class="meta">{detail.id} · {proj?.name ?? ''}</span></p>
      <InvisibleNotice text={detail.plan ?? ''} />
      {#if asking}
        <p class="asking"><Icon name="question" size={14} /><span>{t('decision.alsoAsks')}</span><a href={href({ name: 'task', project: projectId, id: taskId, tab: 'agent' })} onclick={() => (open = false)}>{t('decision.answer')}</a></p>
      {/if}
      {#if detail.planProblems.length}
        <ul class="problems" role="alert">
          {#each detail.planProblems as p, i (i)}<li><Icon name="alert" size={13} />{p.section ? `${p.section}: ` : ''}{p.message}</li>{/each}
        </ul>
      {/if}
      <section class="brief" aria-labelledby="brief-title">
        <h3 id="brief-title">{t('decision.brief')}</h3>
        {#if brief}<Markdown source={brief} />{:else}<p class="muted">{t('decision.noPlan')}</p>{/if}
      </section>
      <p class="code"><span>{t('decision.planCode')}</span><code>{code}</code><small>{t(sections === 1 ? 'decision.whole.one' : 'decision.whole.other', { n: sections })}</small></p>
      <details class="full" ontoggle={(e) => e.currentTarget.open && (read = true)}>
        <summary>{t('decision.fullPlan')}</summary>
        <Markdown source={detail.plan ?? ''} />
      </details>
    {:else if error}
      <p class="err" role="alert"><Icon name="alert" size={14} />{error}</p>
    {:else}
      <p class="muted">{t('app.loading')}</p>
    {/if}
  </div>
  {#snippet footer()}
    <div class="acts">
      <button type="button" class="btn primary" disabled={!detail?.planSha256 || !read} aria-describedby={read ? undefined : 'read-first'} onclick={approve}><Icon name="shield" size={14} />{t('action.approve-plan')}</button>
      {#if detail && !read}<p id="read-first" class="hint">{t('decision.readFirst')}</p>{/if}
      <button type="button" class="btn" onclick={openTask}>{t('decision.openTask')}</button>
      <button type="button" class="btn" onclick={sendBack}>{t('action.send-back')}</button>
    </div>
  {/snippet}
</Modal>

<style>
  .sheet {
    display: grid;
    gap: 12px;
  }
  .ctx {
    display: grid;
    gap: 2px;
    margin: 0;
  }
  .ctx strong {
    font-size: 16px;
  }
  .meta {
    font-size: 12.5px;
    color: var(--text-3);
  }
  .asking {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px 8px;
    margin: 0;
    padding: 8px 10px;
    border-radius: var(--radius-sm);
    background: var(--warn-bg);
    color: var(--warn);
    font-size: 13px;
  }
  .asking span {
    flex: 1;
    min-width: 12em;
  }
  .asking a {
    min-height: 44px;
    display: inline-flex;
    align-items: center;
    font-weight: 600;
    color: inherit;
    text-decoration: underline;
    text-underline-offset: 2px;
  }
  .problems {
    margin: 0;
    padding: 8px 10px;
    list-style: none;
    border-radius: var(--radius-sm);
    background: var(--err-bg);
    color: var(--err);
    font-size: 13px;
    display: grid;
    gap: 4px;
  }
  .problems li {
    display: flex;
    gap: 6px;
    align-items: flex-start;
  }
  .brief {
    display: grid;
    gap: 4px;
    padding: 10px 12px;
    border-radius: var(--radius);
    background: var(--surface-2);
  }
  h3 {
    margin: 0;
    font-size: 12px;
    font-weight: 600;
    color: var(--text-2);
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .code {
    flex-wrap: wrap;
    display: flex;
    align-items: baseline;
    gap: 8px;
    margin: 0;
    font-size: 13px;
    color: var(--text-2);
  }
  .code code {
    font-family: var(--mono);
    font-size: 16px;
    font-weight: 700;
    letter-spacing: 0.06em;
    color: var(--text);
  }
  .full summary {
    cursor: pointer;
    list-style: none;
    font-size: 13px;
    font-weight: 600;
    color: var(--text-2);
    padding: 12px 0;
  }
  .full summary::-webkit-details-marker {
    display: none;
  }
  .full summary::before {
    content: '▸';
    display: inline-block;
    width: 1.1em;
    font-size: 16px;
    color: var(--text);
  }
  .full[open] summary::before {
    content: '▾';
  }
  .acts {
    flex: 1;
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 8px;
  }
  .acts .btn {
    min-height: 44px;
    justify-content: center;
  }
  .acts .primary,
  .hint {
    grid-column: 1 / -1;
  }
  .hint {
    margin: -2px 0 0;
    font-size: 12.5px;
    color: var(--text-2);
    text-align: center;
  }
  .code small {
    flex-basis: 100%;
    font-size: 12px;
    color: var(--text-3);
  }
  .err {
    display: flex;
    align-items: center;
    gap: 6px;
    color: var(--err);
  }
</style>
