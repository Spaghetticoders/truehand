<script lang="ts">
  import { untrack } from 'svelte'
  import Modal from '../components/Modal.svelte'
  import { api, TASK_STATES, type TaskState } from '../api'
  import { t } from '../i18n/index.svelte'
  import { taskOf } from '../stores/app.svelte'
  import { errorText, toast } from '../stores/actions.svelte'
  import { ui } from '../stores/ui.svelte'

  let to = $state<TaskState>('planned')
  let reason = $state('')
  let tellAgent = $state(true)

  const task = $derived(ui.sendBack ? taskOf(ui.sendBack.pid, ui.sendBack.id) : undefined)
  const earlier = $derived(task ? TASK_STATES.slice(0, TASK_STATES.indexOf(task.state)) : [])
  let open = $state(false)

  $effect(() => {
    open = !!ui.sendBack
    if (ui.sendBack)
      untrack(() => {
        to = earlier[earlier.length - 1] ?? 'planned'
        reason = ''
      })
  })

  async function submit() {
    if (!ui.sendBack || !reason.trim()) return
    const { pid, id } = ui.sendBack
    try {
      const r = await api.act(pid, id, 'send-back', { state: to, reason: reason.trim() })
      if (r.sessionId && tellAgent) await api.addComment(pid, id, t('sendBack.agentNote', { state: t(`state.${to}`) }), r.sessionId)
      toast('ok', t('task.sentBack', { state: t(`state.${to}`) }))
      ui.sendBack = null
    } catch (e) {
      toast('error', errorText(e))
    }
  }
</script>

<Modal bind:open title={t('task.sendBackTitle')} onsubmit={submit} onclose={() => (ui.sendBack = null)}>
  <label class="field">
    <span>{t('task.sendBackTo')}</span>
    <select class="input" bind:value={to}>
      {#each earlier as s (s)}<option value={s}>{t(`state.${s}`)}</option>{/each}
    </select>
  </label>
  <label class="field">
    <span>{t('task.sendBackReason')}</span>
    <textarea class="input" rows="4" bind:value={reason} required placeholder={t('task.sendBackPlaceholder')}></textarea>
    <small>{t('task.sendBackHint')}</small>
  </label>
  <label class="check">
    <input type="checkbox" bind:checked={tellAgent} />
    <span>{t('sendBack.tellAgent')}</span>
  </label>
  {#snippet footer()}
    <button type="button" class="btn" onclick={() => (ui.sendBack = null)}>{t('common.cancel')}</button>
    <button type="submit" class="btn primary" disabled={!reason.trim()}>{t('task.sendBack')}</button>
  {/snippet}
</Modal>

<style>
  .check {
    display: flex;
    gap: 8px;
    align-items: center;
    font-size: 13px;
    color: var(--text-2);
  }
</style>
