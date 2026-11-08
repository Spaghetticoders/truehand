<script lang="ts">
  import Icon from '../components/Icon.svelte'
  import Modal from '../components/Modal.svelte'
  import { api, type Passkey } from '../api'
  import { addApprover, approverId, takenIds } from '../approverYaml'
  import { t } from '../i18n/index.svelte'
  import { app, clientName, noteQueued } from '../stores/app.svelte'
  import { go } from '../stores/router.svelte'
  import { errorText, toast } from '../stores/actions.svelte'

  let { open = $bindable(false), passkey }: { open?: boolean; passkey: Passkey | null } = $props()

  const projects = $derived(app.data?.projects.filter((p) => p.active && !p.configError) ?? [])
  let pid = $state('')
  let busy = $state(false)
  let error = $state<string | null>(null)

  $effect(() => {
    if (open && !projects.some((p) => p.id === pid)) pid = projects[0]?.id ?? ''
    if (open) error = null
  })

  const keyBody = (pem: string) => pem.replace(/-----[^-]+-----/g, '').replace(/\s+/g, '')

  async function add() {
    if (!passkey || !pid) return
    busy = true
    error = null
    try {
      const a = await api.activation(pid)
      const flat = a.configYaml.replace(/[\s|]+/g, '')
      if (flat.includes(keyBody(passkey.publicKey))) {
        error = t('passkey.approver.already')
        return
      }
      const yaml = addApprover(a.configYaml, passkey, approverId(passkey.label, takenIds(a.configYaml)))
      const r = await api.putConfig(pid, yaml)
      open = false
      if ('queued' in r) {
        if (r.queuedId) noteQueued({ id: r.queuedId, kind: 'config', projectId: pid, taskId: '', device: t('queuedKind.thisDevice'), time: new Date().toISOString() })
        toast('queued', t('activate.queued'))
      } else {
        toast('ok', t('passkey.approver.added'))
        go({ name: 'activate', project: pid })
      }
    } catch (e) {
      error = errorText(e)
    } finally {
      busy = false
    }
  }
</script>

<Modal bind:open title={t('passkey.approver.title')} onsubmit={add}>
  <div class="body">
    {#if passkey}
      <p class="who">
        <strong>{passkey.label}</strong>
        <span class="chip {passkey.synced ? 'warn' : 'ok'}">{t(passkey.synced ? 'passkey.synced' : 'passkey.bound')}</span>
        <code>{passkey.alg}</code>
      </p>
      {#if passkey.synced}<p class="notice small">{t('passkey.syncedRisk')}</p>{/if}
    {/if}
    <p class="muted small">{t('passkey.approver.hint')}</p>
    {#if projects.length}
      <label class="field">
        <span>{t('passkey.approver.project')}</span>
        <select class="input" bind:value={pid}>
          {#each projects as p (p.id)}<option value={p.id}>{clientName(p.clientId)} / {p.name}</option>{/each}
        </select>
      </label>
      <ol class="steps small">
        <li>{t('passkey.approver.step1')}</li>
        <li>{t('passkey.approver.step2')}</li>
        <li>{t('passkey.approver.step3')}</li>
      </ol>
    {:else}
      <p class="muted small">{t('passkey.approver.noProjects')}</p>
    {/if}
    {#if error}<p class="err" role="alert"><Icon name="alert" size={14} />{error}</p>{/if}
  </div>
  {#snippet footer()}
    <button type="button" class="btn" onclick={() => (open = false)}>{t('common.cancel')}</button>
    <button type="submit" class="btn primary" disabled={busy || !pid}><Icon name="shield" size={14} />{t('passkey.approver.submit')}</button>
  {/snippet}
</Modal>

<style>
  .body {
    display: grid;
    gap: 12px;
  }
  .small {
    font-size: 12.5px;
  }
  .steps {
    margin: 0;
    padding-left: 20px;
    display: grid;
    gap: 4px;
    color: var(--text-2);
  }
  .who {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px 8px;
    margin: 0;
  }
  .who code {
    font-size: 11.5px;
    color: var(--text-3);
  }
  .chip {
    font-size: 11px;
    font-weight: 600;
    padding: 1px 7px;
    border-radius: 10px;
  }
  .chip.ok {
    background: var(--ok-bg);
    color: var(--ok);
  }
  .chip.warn {
    background: var(--warn-bg);
    color: var(--warn);
  }
  .notice {
    margin: 0;
    padding: 8px 10px;
    border-radius: var(--radius-sm);
    background: var(--warn-bg);
    color: var(--warn);
  }
  .err {
    display: flex;
    align-items: center;
    gap: 6px;
    margin: 0;
    color: var(--err);
    font-size: 13px;
  }
</style>
