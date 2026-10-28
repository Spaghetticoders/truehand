<script lang="ts">
  import Modal from '../components/Modal.svelte'
  import Icon from '../components/Icon.svelte'
  import { api, type DirEntry } from '../api'
  import { shell } from '../api/http'
  import { t } from '../i18n/index.svelte'
  import { app } from '../stores/app.svelte'
  import { errorText } from '../stores/actions.svelte'
  import { go } from '../stores/router.svelte'
  import { ui } from '../stores/ui.svelte'

  let path = $state('')
  let client = $state('')
  let name = $state('')
  let machineId = $state('local')
  let dirs = $state<DirEntry[]>([])
  let error = $state<string | null>(null)

  const clients = $derived(app.data?.clients ?? [])
  const machines = $derived((app.data?.machines ?? []).filter((m) => m.status === 'online'))
  const native = $derived(machineId === 'local' && !!shell())

  $effect(() => {
    if (ui.addProject) {
      path = client = name = ''
      machineId = 'local'
      error = null
    }
  })

  $effect(() => {
    const p = path
    const mid = machineId
    if (!ui.addProject) return
    // The server lists one absolute folder (empty = home): suggest the folder being typed into.
    const dir = p.startsWith('/') ? p.slice(0, p.lastIndexOf('/') + 1) : ''
    const timer = setTimeout(() => void api.fsDirs(dir, mid).then((d) => (dirs = d)).catch(() => (dirs = [])), 150)
    return () => clearTimeout(timer)
  })

  async function pick() {
    const p = await api.pickFolder()
    if (p) path = p
  }

  async function submit() {
    if (!path.trim() || !client.trim()) return
    error = null
    try {
      const p = await api.addProject({ path: path.trim(), clientName: client.trim(), name: name.trim() || undefined, machineId })
      ui.addProject = false
      go(p.active ? { name: 'board', project: p.id } : { name: 'activate', project: p.id })
    } catch (e) {
      error = errorText(e)
    }
  }
</script>

<Modal bind:open={ui.addProject} title={t('addProject.title')} onsubmit={submit}>
  {#if machines.length > 1}
    <label class="field">
      <span>{t('addProject.machine')}</span>
      <select class="input" bind:value={machineId}>
        {#each machines as m (m.id)}<option value={m.id}>{m.label}{m.id === 'local' ? ` · ${t('machine.local')}` : ''}</option>{/each}
      </select>
    </label>
  {/if}
  <label class="field">
    <span>{t('addProject.path')}</span>
    <div class="path-row">
      <input class="input mono" bind:value={path} list="dirs" placeholder={t('addProject.pathPlaceholder')} autocomplete="off" required />
      {#if native}<button type="button" class="btn" onclick={pick}><Icon name="folder" size={14} />{t('addProject.choose')}</button>{/if}
    </div>
    <datalist id="dirs">
      {#each dirs.filter((d) => d.isGit) as d (d.path)}<option value={d.path}>{d.name}</option>{/each}
    </datalist>
    <small>{machineId === 'local' ? t('addProject.pathHint') : t('addProject.pathHintRemote')}</small>
  </label>
  <label class="field">
    <span>{t('addProject.client')}</span>
    <input class="input" bind:value={client} list="clients" placeholder={t('addProject.clientPlaceholder')} required />
    <datalist id="clients">
      {#each clients as c (c.id)}<option value={c.name}></option>{/each}
    </datalist>
  </label>
  <label class="field">
    <span>{t('addProject.name')}</span>
    <input class="input" bind:value={name} placeholder={t('addProject.namePlaceholder')} />
  </label>
  {#if error}<p class="err">{error}</p>{/if}
  {#snippet footer()}
    <button type="button" class="btn" onclick={() => (ui.addProject = false)}>{t('common.cancel')}</button>
    <button type="submit" class="btn primary" disabled={!path.trim() || !client.trim()}>{t('addProject.add')}</button>
  {/snippet}
</Modal>

<style>
  .path-row {
    display: flex;
    gap: 8px;
  }
  .path-row .btn {
    height: auto;
  }
  .err {
    color: var(--err);
    font-size: 13px;
  }
</style>
