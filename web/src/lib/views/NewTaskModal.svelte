<script lang="ts">
  import { untrack } from 'svelte'
  import Modal from '../components/Modal.svelte'
  import { api, ApiError } from '../api'
  import { t } from '../i18n/index.svelte'
  import { app, clientName, isOffline } from '../stores/app.svelte'
  import { errorText, toast } from '../stores/actions.svelte'
  import { go } from '../stores/router.svelte'
  import { ui } from '../stores/ui.svelte'

  let projectId = $state('')
  let title = $state('')
  let description = $state('')
  let id = $state('')
  let source = $state('')
  let scope = $state('')
  let worktree = $state(true)
  let error = $state<string | null>(null)
  let errorCode = $state<string | undefined>()
  let busy = $state(false)

  const projects = $derived(app.data?.projects.filter((p) => p.active && !isOffline(p.id)) ?? [])

  // Reset only when the dialog opens: background refreshes must not wipe what is being typed.
  $effect(() => {
    if (ui.newTask)
      untrack(() => {
        projectId = projects.find((p) => p.id === ui.newTaskProject)?.id ?? projects[0]?.id ?? ''
        title = description = id = source = scope = ''
        worktree = true
        error = null
      })
  })

  let failedWithWorktree = $state(false)

  async function submit() {
    if (!title.trim() || !projectId) return
    failedWithWorktree = false
    busy = true
    error = null
    errorCode = undefined
    try {
      const r = await api.createTask(projectId, {
        title: title.trim(),
        description: description.trim() || undefined,
        id: id.trim() || undefined,
        externalTicket: source.trim() || undefined,
        scope: scope.split('\n').map((s) => s.trim()).filter(Boolean),
        worktree,
      })
      ui.newTask = false
      if (r.ticketFetchError) toast('error', t('newTask.ticketError', { error: r.ticketFetchError }))
      go({ name: 'task', project: projectId, id: r.id, tab: 'description' })
    } catch (e) {
      error = errorText(e)
      errorCode = e instanceof ApiError ? e.code : undefined
      failedWithWorktree = worktree
    } finally {
      busy = false
    }
  }
</script>

<Modal bind:open={ui.newTask} title={t('newTask.title')} onsubmit={submit}>
  <label class="field">
    <span>{t('newTask.project')}</span>
    <select class="input" bind:value={projectId}>
      {#each projects as p (p.id)}
        <option value={p.id}>{clientName(p.clientId)} · {p.name}</option>
      {/each}
    </select>
  </label>
  <label class="field">
    <span>{t('newTask.name')}</span>
    <input class="input" bind:value={title} placeholder={t('newTask.namePlaceholder')} required />
  </label>
  <label class="field">
    <span>{t('newTask.description')}</span>
    <textarea class="input" rows="6" bind:value={description} placeholder={t('newTask.descriptionPlaceholder')}></textarea>
  </label>
  <label class="field">
    <span>{t('newTask.ticket')}</span>
    <input class="input mono" bind:value={source} placeholder={t('newTask.ticketPlaceholder')} />
    <small>{t('newTask.ticketHint')}</small>
  </label>
  <details>
    <summary>{t('newTask.advanced')}</summary>
    <div class="adv">
      <label class="field">
        <span>{t('newTask.id')}</span>
        <input class="input mono" bind:value={id} />
        <small>{t('newTask.idHint')}</small>
      </label>
      <label class="check">
        <input type="checkbox" bind:checked={worktree} />
        <span>{t('newTask.worktree')}<small>{t('newTask.worktreeHint')}</small></span>
      </label>
      <label class="field full">
        <span>{t('newTask.scope')}</span>
        <textarea class="input mono" rows="3" bind:value={scope}></textarea>
        <small>{t('newTask.scopeHint')}</small>
      </label>
    </div>
  </details>
  {#if error}
    <div class="err-box">
      <p class="err">{error}</p>
      {#if errorCode === 'config-not-committed'}
        <button type="button" class="btn sm primary" onclick={() => ((ui.newTask = false), go({ name: 'activate', project: projectId }))}>{t('newTask.commitConfig')}</button>
      {/if}
      {#if failedWithWorktree}
        <button type="button" class="btn sm" onclick={() => ((worktree = false), submit())}>{t('newTask.withoutWorktree')}</button>
      {/if}
    </div>
  {/if}
  {#snippet footer()}
    <button type="button" class="btn" onclick={() => (ui.newTask = false)}>{t('common.cancel')}</button>
    <button type="submit" class="btn primary" disabled={!title.trim() || !projectId || busy}>{t('newTask.create')}</button>
  {/snippet}
</Modal>

<style>
  summary {
    cursor: pointer;
    font-size: 13px;
    font-weight: 600;
    color: var(--text-2);
  }
  .adv {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 14px;
    margin-top: 12px;
  }
  .full {
    grid-column: 1 / -1;
  }
  .check {
    display: flex;
    gap: 8px;
    align-items: flex-start;
    font-size: 13px;
    padding-top: 22px;
  }
  .check small {
    display: block;
    color: var(--text-3);
    font-size: 12px;
  }
  .err-box {
    display: grid;
    gap: 8px;
    justify-items: start;
    padding: 10px 12px;
    border-radius: var(--radius-sm);
    background: var(--err-bg);
  }
  .err {
    color: var(--err);
    font-size: 13px;
    overflow-wrap: anywhere;
  }
  @media (max-width: 640px) {
    .adv {
      grid-template-columns: 1fr;
    }
    .check {
      padding-top: 0;
    }
  }
</style>
